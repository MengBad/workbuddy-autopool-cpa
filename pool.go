// pool.go implements the international (Global) account pool with a hard
// minimum-availability guarantee.
//
// The upstream plugin already fails a single request over to another account
// when one answers 429 / 6004 / 14018. That keeps ONE request alive, but it
// does not keep the POOL alive: once every Global account drifts into
// exhausted/quarantine, requests start failing with 429 and nothing refills the
// pool. This file adds the missing layer:
//
//  1. a live view of every workbuddy credential (state, credits, quarantine),
//  2. a min_available_accounts invariant that is re-evaluated on every observed
//     failure and on every monitor tick,
//  3. persistence of quarantine windows + credit snapshots across restarts
//     (workbuddy-pool.json in the same data dir as workbuddy-state.json).
//
// Identity: the host re-issues auth_index on every restart, so persisted state
// is keyed by the account UID (falling back to auth id / file name). Host list
// is the source of truth for which accounts EXIST; the pool file is the source
// of truth for what we LEARNED about them.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	poolStateFileName = "workbuddy-pool.json"

	// poolState values surfaced in the panel and used for accounting.
	poolStateActive      = "active"      // eligible to serve
	poolStateExhausted   = "exhausted"   // upstream said "credits exhausted" (14018 / 402)
	poolStateQuarantined = "quarantined" // manual or risk-control park, no credits signal
	poolStateDisabled    = "disabled"    // disabled in the host auth store
	poolStateInvalid     = "invalid"     // credential unreadable / token dead
	// poolStateSessionDead means the credential parses and the JWT is not yet
	// expired, but upstream rejects it (APISIX 401 / 12153). This is NOT "zero
	// credits": the balance is unknown and unfixable without a fresh login, so
	// it must never be reported as "积分 0" or counted as available.
	poolStateSessionDead = "session_dead"
)

// Pool scope: the requirement targets the international realm. CN accounts are
// still tracked (the panel shows them) but do not count toward the minimum
// unless pool_realm is set to "any".
const (
	poolRealmGlobal = "global"
	poolRealmAny    = "any"

	// poolCNRegion is the accountRegion() value of a domestic credential.
	poolCNRegion = "cn"
)

// Test seams. Production code never reassigns these; the unit tests replace
// them because the host RPC bridge (host.auth.*) does not exist outside CPA.
var (
	poolSeamsMu sync.RWMutex

	poolAuthListSource   = hostAuthList
	poolAuthBundleSource = hostAuthGetBundle
	// poolAuthSaveSource persists a credential. Stubbed in tests (no host RPC).
	poolAuthSaveSource = hostAuthSaveJSONResponse
	// poolTrialSource claims the one-time expert pack. Stubbed in tests.
	poolTrialSource = performTrialCall
	// poolCreditsSource reads the credit snapshot. Stubbed in tests.
	poolCreditsSource = fetchUserResource
)

// The seams are read from background goroutines (the monitor tick and the
// refill goroutine started by a failure observation), so every access goes
// through these accessors: assigning a func var while another goroutine reads it
// is a data race even though production code never reassigns anything.

func poolListAuths() ([]pluginapi.HostAuthFileEntry, error) {
	poolSeamsMu.RLock()
	fn := poolAuthListSource
	poolSeamsMu.RUnlock()
	return fn()
}

func poolGetAuthBundle(authIndex string) (*storedAuth, *hostAuthPhysical, error) {
	poolSeamsMu.RLock()
	fn := poolAuthBundleSource
	poolSeamsMu.RUnlock()
	return fn(authIndex)
}

func poolSaveAuthFile(name string, raw []byte) (pluginapi.HostAuthSaveResponse, error) {
	poolSeamsMu.RLock()
	fn := poolAuthSaveSource
	poolSeamsMu.RUnlock()
	return fn(name, raw)
}

func poolClaimTrial(sa *storedAuth) (map[string]any, error) {
	poolSeamsMu.RLock()
	fn := poolTrialSource
	poolSeamsMu.RUnlock()
	return fn(sa)
}

func poolFetchCredits(sa *storedAuth) (*creditsSummary, error) {
	poolSeamsMu.RLock()
	fn := poolCreditsSource
	poolSeamsMu.RUnlock()
	return fn(sa)
}

// poolConfigYAML extracts the plugin config YAML out of a lifecycle request.
// The host may hand the subtree either as a plain string or — because the wire
// type is []byte and encoding/json base64-encodes byte slices — as base64.
// Both shapes are accepted so a host-side encoding change cannot silently
// disable every pool setting.
func poolConfigYAML(raw []byte) []byte {
	if len(raw) == 0 {
		return nil
	}
	var req struct {
		ConfigYAML json.RawMessage `json:"config_yaml"`
	}
	if err := json.Unmarshal(raw, &req); err != nil || len(req.ConfigYAML) == 0 {
		return nil
	}
	// Try []byte first: the wire type is []byte, so a base64 string decodes
	// here. A host that sends the YAML as a plain JSON string fails base64
	// decoding (YAML with newlines/colons is not valid base64) and falls through
	// to the string branch.
	var asBytes []byte
	if err := json.Unmarshal(req.ConfigYAML, &asBytes); err == nil && len(asBytes) > 0 {
		return asBytes
	}
	var asString string
	if err := json.Unmarshal(req.ConfigYAML, &asString); err == nil {
		return []byte(asString)
	}
	return nil
}

// poolConfig is the runtime configuration of the pool manager. Every field has
// a env-var fallback so a container can be tuned without editing config.yaml.
type poolConfig struct {
	// MinAvailable is the hard floor of usable accounts in the pool realm.
	MinAvailable int
	// Realm scopes which accounts count toward MinAvailable.
	Realm string
	// CheckInterval is the monitor tick (credit refresh + invariant check).
	CheckInterval time.Duration
	// QuarantineMinutes is the default park window for exhausted accounts.
	QuarantineMinutes int
}

// defaultPoolConfig: 2 available accounts, refill from a 2-minute monitor.
func defaultPoolConfig() poolConfig {
	return poolConfig{
		MinAvailable:      2,
		Realm:             poolRealmGlobal,
		CheckInterval:     2 * time.Minute,
		QuarantineMinutes: 30,
	}
}

var (
	poolCfgMu sync.RWMutex
	poolCfg   = defaultPoolConfig()
)

// poolConfigSnapshot returns a copy of the live configuration.
func poolConfigSnapshot() poolConfig {
	poolCfgMu.RLock()
	defer poolCfgMu.RUnlock()
	return poolCfg
}

// poolSetMinAvailable is the runtime toggle the panel exposes.
// exposes. They are process-local: plugins.configs.workbuddy wins on restart,
// exactly like checkin_auto / daily_bonus.
func poolSetMinAvailable(n int) {
	if n < 0 {
		n = 0
	}
	poolCfgMu.Lock()
	poolCfg.MinAvailable = n
	poolCfgMu.Unlock()
}

// -----------------------------------------------------------------------------
// Persisted state
// -----------------------------------------------------------------------------

// poolAccountRecord is what the plugin remembers about one account between
// restarts. Everything here is a LEARNING (quarantine window, last credit
// snapshot, last observed state); existence comes from the host auth store.
type poolAccountRecord struct {
	UID             string    `json:"uid,omitempty"`
	Nickname        string    `json:"nickname,omitempty"`
	Name            string    `json:"name,omitempty"`
	Region          string    `json:"region,omitempty"`
	State           string    `json:"state,omitempty"`
	Reason          string    `json:"reason,omitempty"`
	QuarantineUntil time.Time `json:"quarantine_until,omitempty"`
	Remain          int64     `json:"remain"`
	Used            int64     `json:"used"`
	Size            int64     `json:"size"`
	CreditsKnown    bool      `json:"credits_known"`
	// CreditsErr means the last balance read failed. A failed read is NOT a
	// healthy account: the pool must not advertise capacity it cannot prove.
	CreditsErr     string    `json:"credits_err,omitempty"`
	CreditsAt      time.Time `json:"credits_at,omitempty"`
	TokenExpiresAt int64     `json:"token_expires_at,omitempty"`
	CanRefresh     bool      `json:"can_refresh,omitempty"`
	UpdatedAt      time.Time `json:"updated_at,omitempty"`
	Failures       int       `json:"failures,omitempty"`
}

// registerAttempt is one line of the auto-registration audit trail.
type registerAttempt struct {
	At     time.Time `json:"at"`
	Mode   string    `json:"mode,omitempty"`
	Result string    `json:"result"`
	Detail string    `json:"detail,omitempty"`
	UID    string    `json:"uid,omitempty"`
	File   string    `json:"file,omitempty"`
}

type poolPersistFile struct {
	Accounts  map[string]*poolAccountRecord `json:"accounts"`
	LastCheck time.Time                     `json:"last_check,omitempty"`
}

var (
	poolStateMu   sync.Mutex
	poolStatePath = ""
	poolState     *poolPersistFile
)

// poolFile returns the on-disk path of workbuddy-pool.json.
func poolFilePath() string {
	return filepath.Join(wbDataDir(), poolStateFileName)
}

// poolLoad returns the cached persisted state, reading it once per path.
// poolLoad returns a COPY of the persisted state.
//
// The copy matters: poolMutate rewrites this state under poolStateMu from
// whatever goroutine observed a failure, so a caller that kept the cache
// pointer and then walked its maps would read a map being written (a real data
// race, and a hard crash in Go). Callers that only need one field still go
// through here — the copy is tiny (a handful of accounts).
func poolLoad() *poolPersistFile {
	poolStateMu.Lock()
	defer poolStateMu.Unlock()
	return clonePoolPersist(poolLoadLocked())
}

// clonePoolPersist duplicates the state file including every account record.
func clonePoolPersist(in *poolPersistFile) *poolPersistFile {
	if in == nil {
		return &poolPersistFile{Accounts: map[string]*poolAccountRecord{}}
	}
	out := &poolPersistFile{
		Accounts:  make(map[string]*poolAccountRecord, len(in.Accounts)),
		LastCheck: in.LastCheck,
	}
	for k, v := range in.Accounts {
		if v == nil {
			continue
		}
		cp := *v
		out.Accounts[k] = &cp
	}
	return out
}

func poolLoadLocked() *poolPersistFile {
	path := poolFilePath()
	if poolState != nil && poolStatePath == path {
		return poolState
	}
	st := &poolPersistFile{Accounts: map[string]*poolAccountRecord{}}
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		var parsed poolPersistFile
		if jsonErr := json.Unmarshal(raw, &parsed); jsonErr == nil {
			if parsed.Accounts == nil {
				parsed.Accounts = map[string]*poolAccountRecord{}
			}
			st = &parsed
		} else {
			// Never overwrite a corrupt file: keep it for inspection.
			_ = os.Rename(path, path+".corrupt")
		}
	case os.IsNotExist(err):
	default:
		guardLogf("pool: read %s: %v", path, err)
	}
	poolState = st
	poolStatePath = path
	return st
}

// poolMutate applies fn to the persisted state and rewrites the file.
func poolMutate(fn func(*poolPersistFile) error) error {
	poolStateMu.Lock()
	defer poolStateMu.Unlock()
	st := poolLoadLocked()
	if err := fn(st); err != nil {
		return err
	}
	if st.Accounts == nil {
		st.Accounts = map[string]*poolAccountRecord{}
	}
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := atomicWriteFile(poolFilePath(), raw); err != nil {
		guardLogf("pool: write %s: %v", poolFilePath(), err)
		return err
	}
	return nil
}

// poolKey identifies an account across restarts: UID first, then the stable
// auth id, then the physical file name.
func poolKey(uid, authID, name string) string {
	if v := strings.TrimSpace(uid); v != "" {
		return "uid:" + v
	}
	if v := strings.TrimSpace(authID); v != "" {
		return "id:" + v
	}
	return "name:" + strings.TrimSpace(name)
}

// -----------------------------------------------------------------------------
// Live pool view
// -----------------------------------------------------------------------------

// poolAccount is one row of the live pool view (host list ∪ persisted learning).
type poolAccount struct {
	Key             string    `json:"key"`
	AuthIndex       string    `json:"auth_index"`
	AuthID          string    `json:"auth_id,omitempty"`
	Name            string    `json:"name"`
	UID             string    `json:"uid,omitempty"`
	Nickname        string    `json:"nickname,omitempty"`
	Label           string    `json:"label,omitempty"`
	Region          string    `json:"region"`
	State           string    `json:"state"`
	Reason          string    `json:"reason,omitempty"`
	Disabled        bool      `json:"disabled"`
	QuarantineUntil time.Time `json:"quarantine_until,omitempty"`
	Remain          int64     `json:"remain"`
	Used            int64     `json:"used"`
	Size            int64     `json:"size"`
	CreditsKnown    bool      `json:"credits_known"`
	// CreditsErr means the last balance read failed. A failed read is NOT a
	// healthy account: the pool must not advertise capacity it cannot prove.
	CreditsErr     string    `json:"credits_err,omitempty"`
	CreditsAt      time.Time `json:"credits_at,omitempty"`
	TokenExpiresAt int64     `json:"token_expires_at,omitempty"`
	CanRefresh     bool      `json:"can_refresh"`
	InFlight       int       `json:"in_flight"`
	Available      bool      `json:"available"`
	Failures       int       `json:"failures,omitempty"`
	Error          string    `json:"error,omitempty"`
}

// poolCountsToward reports whether the account counts toward MinAvailable.
func poolCountsToward(a *poolAccount, cfg poolConfig) bool {
	if a == nil {
		return false
	}
	if cfg.Realm == poolRealmAny {
		return true
	}
	return a.Region == realmGlobal
}

// poolEntryAvailable is the "available" definition from the requirement:
// not disabled, not quarantined, credits > 0 (or still callable because we have
// no negative signal), and the token is either live or refreshable.
func poolEntryAvailable(a *poolAccount, cfg poolConfig, now time.Time) bool {
	if a == nil || a.Disabled {
		return false
	}
	switch a.State {
	case poolStateDisabled, poolStateInvalid, poolStateExhausted, poolStateSessionDead:
		return false
	}
	if a.QuarantineUntil.After(now) {
		return false
	}
	// A live token is not the same thing as "usable": an account with zero
	// credits answers 14018 on every call. Only a positive balance, or an
	// account we genuinely have no credit reading for AND that has never been
	// observed failing, counts as available.
	if a.CreditsKnown {
		if a.Remain <= 0 {
			return false
		}
	} else if a.CreditsErr != "" || a.Failures > 0 {
		// No usable balance reading (read failed) or a failure on record: do not
		// claim capacity we cannot prove. Being unable to read the balance is
		// itself a signal — it usually means the credential is no longer accepted
		// by the billing endpoint.
		return false
	}
	if a.TokenExpiresAt > 0 && a.TokenExpiresAt <= now.Unix() && !a.CanRefresh {
		return false
	}
	_ = cfg
	return true
}

// poolSnapshot reconciles the host auth store with the persisted learning and
// returns the live pool plus the number of available accounts in scope.
//
// It performs NO upstream calls: credits come from the shared per-account cache
// (populated by the monitor tick / panel refresh), so a snapshot is cheap enough
// to build on every failure observation.
func poolSnapshot() (accounts []*poolAccount, available int) {
	cfg := poolConfigSnapshot()
	now := time.Now()
	files, err := poolListAuths()
	if err != nil {
		guardLogf("pool: host.auth.list: %v", err)
		return nil, 0
	}
	persist := poolLoad()
	inFlight := accountInFlightSnapshot()
	for _, f := range files {
		acct := &poolAccount{
			AuthIndex: f.AuthIndex,
			AuthID:    f.ID,
			Name:      f.Name,
			Label:     f.Label,
			Disabled:  listAuthEntryDisabled(f),
		}
		sa, phys, getErr := poolGetAuthBundle(f.AuthIndex)
		if getErr != nil {
			acct.State = poolStateInvalid
			acct.Reason = "read auth: " + truncateRedacted(getErr.Error(), 120)
			accounts = append(accounts, acct)
			continue
		}
		if phys != nil {
			acct.Disabled = acct.Disabled || phys.Disabled
			if phys.Name != "" {
				acct.Name = phys.Name
			}
		}
		acct.UID = sa.Account.UID
		acct.Nickname = sa.Account.Nickname
		acct.Region = accountRegion(sa)
		acct.TokenExpiresAt = sa.Auth.ExpiresAt
		acct.CanRefresh = strings.TrimSpace(sa.Auth.RefreshToken) != ""
		acct.Key = poolKey(acct.UID, acct.AuthID, acct.Name)
		acct.InFlight = inFlight[accountGateKey(&failoverCandidate{AuthID: f.AuthIndex, Auth: sa})]

		// Merge the persisted learning.
		if rec := persist.Accounts[acct.Key]; rec != nil {
			acct.QuarantineUntil = rec.QuarantineUntil
			acct.Remain, acct.Used, acct.Size = rec.Remain, rec.Used, rec.Size
			acct.CreditsKnown = rec.CreditsKnown
			acct.CreditsErr = rec.CreditsErr
			acct.CreditsAt = rec.CreditsAt
			acct.Failures = rec.Failures
			acct.State = rec.State
			acct.Reason = rec.Reason
		}
		// Live cache beats the file: the monitor/panel writes it on every read.
		if v, ok := accountCache.Load(acct.AuthID); ok {
			if e, ok2 := v.(*accountCacheEntry); ok2 && e.credits != nil {
				acct.Remain = e.credits.TotalRemain
				acct.Used = e.credits.TotalUsed
				acct.Size = e.credits.TotalSize
				acct.CreditsKnown = true
				acct.CreditsErr = ""
				acct.CreditsAt = e.fetched
			}
		}
		// Cooldown map is the routing source of truth: an account parked there
		// must not be reported as available even if the file says active.
		if until := authCooldownUntil(f.AuthIndex); !until.IsZero() && until.After(acct.QuarantineUntil) {
			acct.QuarantineUntil = until
			if strings.TrimSpace(acct.Reason) == "" {
				acct.Reason = "routing cooldown"
			}
		}
		switch {
		case acct.Disabled:
			acct.State = poolStateDisabled
		case acct.State == "" || acct.State == poolStateActive:
			acct.State = poolStateActive
		}
		if acct.State == poolStateActive && acct.QuarantineUntil.After(now) {
			acct.State = poolStateQuarantined
		}
		if acct.State == poolStateActive && acct.CreditsKnown && acct.Remain <= 0 {
			acct.State = poolStateExhausted
		}
		acct.Available = poolEntryAvailable(acct, cfg, now)
		if acct.Available && poolCountsToward(acct, cfg) {
			available++
		}
		accounts = append(accounts, acct)
	}
	sort.Slice(accounts, func(i, j int) bool {
		if accounts[i].Region != accounts[j].Region {
			return accounts[i].Region > accounts[j].Region // global first
		}
		return accounts[i].Name < accounts[j].Name
	})
	return accounts, available
}

// poolAvailableCount is the cheap form used by the invariant check.
func poolAvailableCount() (int, []*poolAccount) {
	accounts, available := poolSnapshot()
	return available, accounts
}

// -----------------------------------------------------------------------------
// Observation hooks (called from the failover loop)
// -----------------------------------------------------------------------------

// poolObserveFailure records that an account failed in a way that affects its
// eligibility, and re-evaluates the minimum-availability invariant. It is
// called from markCandidateUnavailable, i.e. before another request can take
// the freed slot.
func poolObserveFailure(c *failoverCandidate, outcome attemptOutcome, body string) {
	if c == nil {
		return
	}
	now := time.Now()
	cfg := poolConfigSnapshot()
	reason := "429"
	state := poolStateQuarantined
	var until time.Time
	switch {
	case isRiskControlResponse(0, body):
		reason = "risk_control"
		until = now.Add(24 * time.Hour)
	case outcome == outcomeAccountExhausted:
		reason = "credits_exhausted"
		state = poolStateExhausted
		until = now.Add(time.Duration(cfg.QuarantineMinutes) * time.Minute)
		if reset := parseRateLimitResetAt(body); reset.After(now) {
			until = reset
		}
	case outcome == outcomeRateLimited:
		reason = "429"
		until = now.Add(time.Duration(cfg.QuarantineMinutes) * time.Minute)
		if reset := parseRateLimitResetAt(body); reset.After(now) {
			until = reset
		}
		if capAt := now.Add(maxRateLimitCooldown); until.After(capAt) {
			until = capAt
		}
	default:
		return
	}
	uid := ""
	if c.Auth != nil {
		uid = c.Auth.Account.UID
	}
	key := poolKey(uid, c.RecordID, firstAlias(c))
	_ = poolMutate(func(st *poolPersistFile) error {
		rec := st.Accounts[key]
		if rec == nil {
			rec = &poolAccountRecord{UID: uid, Name: firstAlias(c)}
			st.Accounts[key] = rec
		}
		rec.State = state
		rec.Reason = reason
		rec.QuarantineUntil = until
		rec.Failures++
		rec.UpdatedAt = now
		return nil
	})
	guardLogf("pool: account %q unavailable reason=%s until=%s failures=%d",
		redactSecrets(firstNonEmpty(uid, c.RecordID, c.AuthID)), reason, until.UTC().Format(time.RFC3339), failureCount(key))
	// Note: the routing cooldown is armed by markCandidateUnavailable, which is
	// the caller of this hook. It scopes a 429 to the offending model; repeating
	// it here would account-wide poison every other model on the same account.
	goPoolEnsure("failure:" + reason)
}

// poolObserveSuccess clears the learning for an account that just served a
// request, so a transient 429 does not keep it parked forever.
func poolObserveSuccess(c *failoverCandidate) {
	if c == nil {
		return
	}
	uid := ""
	if c.Auth != nil {
		uid = c.Auth.Account.UID
	}
	key := poolKey(uid, c.RecordID, firstAlias(c))
	changed := false
	_ = poolMutate(func(st *poolPersistFile) error {
		rec := st.Accounts[key]
		if rec == nil {
			return nil
		}
		if rec.State == poolStateExhausted {
			return nil // an explicit credit rejection is not disproved by one success on another model
		}
		if rec.State == poolStateActive && rec.QuarantineUntil.IsZero() && rec.Failures == 0 {
			return nil
		}
		rec.State = poolStateActive
		rec.Reason = ""
		rec.QuarantineUntil = time.Time{}
		rec.Failures = 0
		rec.UpdatedAt = time.Now()
		changed = true
		return nil
	})
	if changed {
		guardLogf("pool: account %q recovered (success)", redactSecrets(firstNonEmpty(uid, c.AuthID)))
	}
}

// poolObserveCredits records a credit snapshot and derives the state from it.
func poolObserveCredits(authIndex, authID, uid, name, region string, cr *creditsSummary, disabled bool) {
	if cr == nil {
		return
	}
	now := time.Now()
	cfg := poolConfigSnapshot()
	key := poolKey(uid, authID, name)
	exhausted := isCreditsExhausted(cr)
	_ = poolMutate(func(st *poolPersistFile) error {
		rec := st.Accounts[key]
		if rec == nil {
			rec = &poolAccountRecord{UID: uid, Name: name, Region: region}
			st.Accounts[key] = rec
		}
		rec.Region = region
		rec.Remain, rec.Used, rec.Size = cr.TotalRemain, cr.TotalUsed, cr.TotalSize
		rec.CreditsKnown = true
		rec.CreditsErr = ""
		rec.CreditsAt = now
		rec.UpdatedAt = now
		switch {
		case disabled:
			rec.State = poolStateDisabled
		case exhausted:
			rec.State = poolStateExhausted
			rec.Reason = "credits_exhausted"
			if rec.QuarantineUntil.Before(now) {
				rec.QuarantineUntil = now.Add(time.Duration(cfg.QuarantineMinutes) * time.Minute)
			}
		default:
			// A positive balance disproves exhaustion; keep any manual quarantine.
			if rec.State == poolStateExhausted {
				rec.State = poolStateActive
				rec.Reason = ""
				rec.QuarantineUntil = time.Time{}
				rec.Failures = 0
			}
		}
		return nil
	})
}

// poolObserveCreditsError records that the balance read failed for an account.
// This is a first-class signal, not an absence: an account whose balance we
// cannot read is one we cannot promise capacity for, so it stops counting as
// available until a read succeeds again.
func poolObserveCreditsError(authID, uid, name, region, errText string) {
	// A 401 from the billing endpoint means the session is gone upstream, not
	// that the account has no credits. Keep the two apart: one needs a
	// re-login, the other needs a top-up.
	sessionDead := strings.Contains(errText, "401") ||
		strings.Contains(errText, "12153") ||
		strings.Contains(errText, "Authorization Required")
	now := time.Now()
	key := poolKey(uid, authID, name)
	_ = poolMutate(func(st *poolPersistFile) error {
		rec := st.Accounts[key]
		if rec == nil {
			rec = &poolAccountRecord{UID: uid, Name: name, Region: region}
			st.Accounts[key] = rec
		}
		rec.Region = region
		rec.CreditsKnown = false
		rec.CreditsErr = truncateRedacted(errText, 160)
		if sessionDead {
			rec.State = poolStateSessionDead
			rec.Reason = "token rejected by upstream (401/12153) — re-login required"
		}
		rec.UpdatedAt = now
		return nil
	})
}

// poolObserveNewAccount registers a freshly created credential in the pool.
func poolObserveNewAccount(uid, name, region, source string) {
	now := time.Now()
	key := poolKey(uid, "", name)
	_ = poolMutate(func(st *poolPersistFile) error {
		rec := st.Accounts[key]
		if rec == nil {
			rec = &poolAccountRecord{UID: uid, Name: name}
			st.Accounts[key] = rec
		}
		rec.Region = region
		rec.State = poolStateActive
		rec.Reason = ""
		rec.QuarantineUntil = time.Time{}
		rec.Failures = 0
		rec.UpdatedAt = now
		return nil
	})
	guardLogf("pool: account added uid=%q region=%s source=%s", redactSecrets(uid), region, source)
}

// poolQuarantine parks an account for a fixed window (manual rotate/quarantine).
func poolQuarantine(authIndex string, minutes int, reason string) (map[string]any, error) {
	if minutes <= 0 {
		minutes = poolConfigSnapshot().QuarantineMinutes
	}
	sa, phys, err := poolGetAuthBundle(authIndex)
	if err != nil {
		return nil, err
	}
	until := time.Now().Add(time.Duration(minutes) * time.Minute)
	key := poolKey(sa.Account.UID, authIndex, physName(phys, authIndex))
	if err := poolMutate(func(st *poolPersistFile) error {
		rec := st.Accounts[key]
		if rec == nil {
			rec = &poolAccountRecord{UID: sa.Account.UID}
			st.Accounts[key] = rec
		}
		rec.State = poolStateQuarantined
		rec.Reason = reason
		rec.QuarantineUntil = until
		rec.UpdatedAt = time.Now()
		return nil
	}); err != nil {
		return nil, err
	}
	for _, id := range candidateCooldownIDs(&failoverCandidate{AuthID: authIndex, Auth: sa}) {
		markAuthCooldownReason(id, until, "quarantine")
	}
	guardLogf("pool: manual quarantine auth=%q minutes=%d reason=%s", redactSecrets(authIndex), minutes, reason)
	out := map[string]any{
		"success":          true,
		"auth_index":       authIndex,
		"uid":              sa.Account.UID,
		"quarantine_until": until.UTC().Format(time.RFC3339),
		"minutes":          minutes,
		"reason":           reason,
	}
	out["available_after"], _ = poolAvailableCount()
	goPoolEnsure("manual-quarantine")
	return out, nil
}

// poolRelease clears the quarantine for one account.
func poolRelease(authIndex string) (map[string]any, error) {
	sa, phys, err := poolGetAuthBundle(authIndex)
	if err != nil {
		return nil, err
	}
	key := poolKey(sa.Account.UID, authIndex, physName(phys, authIndex))
	if err := poolMutate(func(st *poolPersistFile) error {
		rec := st.Accounts[key]
		if rec == nil {
			return nil
		}
		rec.State = poolStateActive
		rec.Reason = ""
		rec.QuarantineUntil = time.Time{}
		rec.Failures = 0
		rec.UpdatedAt = time.Now()
		return nil
	}); err != nil {
		return nil, err
	}
	for _, id := range candidateCooldownIDs(&failoverCandidate{AuthID: authIndex, Auth: sa}) {
		resetAuthCooldown(id)
	}
	guardLogf("pool: released auth=%q", redactSecrets(authIndex))
	out := map[string]any{"success": true, "auth_index": authIndex, "uid": sa.Account.UID}
	out["available_after"], _ = poolAvailableCount()
	return out, nil
}

// -----------------------------------------------------------------------------
// Minimum-availability invariant + monitor
// -----------------------------------------------------------------------------

var (
	poolEnsureMu        sync.Mutex
	poolEnsureLast      time.Time
	poolEnsuring        bool
	poolRefillAnnounced bool
)

// poolBgActive counts in-flight invariant goroutines. A counter behind a mutex
// (not sync.WaitGroup) is deliberate: WaitGroup forbids Add racing with Wait,
// and a refill can legitimately start while a caller is draining.
var (
	poolBgMu     sync.Mutex
	poolBgActive int
)

// goPoolEnsure runs the invariant check off the request path (the failing
// request must not wait for host.auth.list) while staying observable.
func goPoolEnsure(trigger string) {
	poolBgMu.Lock()
	poolBgActive++
	poolBgMu.Unlock()
	go func() {
		defer func() {
			poolBgMu.Lock()
			poolBgActive--
			poolBgMu.Unlock()
		}()
		poolEnsureMinimum(trigger)
	}()
}

// poolWaitBackground waits (bounded) for in-flight invariant goroutines. Tests
// call it before restoring their seams; the monitor tick calls it before a
// destructive check so two refill decisions never overlap.
func poolWaitBackground() {
	deadline := time.Now().Add(5 * time.Second)
	for {
		poolBgMu.Lock()
		n := poolBgActive
		poolBgMu.Unlock()
		if n == 0 || time.Now().After(deadline) {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// poolEnsureMinimum evaluates the minimum-availability invariant and reports
// it. Automatic account registration was retired in 0.12: creating accounts is
// an operator decision (and upstream gates it behind a browser OAuth login), so
// the plugin only tells the operator when the pool is short — the panel shows
// the shortfall and the register link comes from the panel's own login flow.
func poolEnsureMinimum(trigger string) {
	cfg := poolConfigSnapshot()
	available, _ := poolAvailableCount()
	if available >= cfg.MinAvailable {
		poolSetRefillAnnounced(false)
		return
	}
	// Announce once per dip instead of on every 2-minute tick.
	if !poolRefillAnnounced {
		poolEnsureMu.Lock()
		poolRefillAnnounced = true
		poolEnsureMu.Unlock()
		guardLogf("pool: availability %d/%d below minimum (trigger=%s) — add an account from the panel",
			available, cfg.MinAvailable, trigger)
	}
}

// poolSetRefillAnnounced resets the once-per-dip log flag.
func poolSetRefillAnnounced(v bool) {
	poolEnsureMu.Lock()
	poolRefillAnnounced = v
	poolEnsureMu.Unlock()
}

// poolMonitorLoop refreshes credits and re-checks the invariant on a ticker.
// It is started once from configure() (same lifecycle as the check-in
// scheduler) and never stopped explicitly — plugin shutdown is a documented
// no-op in this codebase.
func poolMonitorLoop(interval time.Duration) {
	if interval <= 0 {
		interval = 2 * time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	// First pass shortly after start: the host auth list is not populated until
	// the registration RPC has been answered.
	timer := time.NewTimer(45 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			poolMonitorTick()
		case <-ticker.C:
			poolMonitorTick()
		}
	}
}

var (
	poolMonitorOnce sync.Once
	poolMonitorMu   sync.Mutex
	poolMonitorRun  bool
	poolMonitorTick = poolMonitorOnceTick
)

// poolMonitorOnceTick guards against overlapping ticks (credit refresh of N
// accounts can take longer than the interval).
func poolMonitorOnceTick() {
	poolMonitorMu.Lock()
	if poolMonitorRun {
		poolMonitorMu.Unlock()
		return
	}
	poolMonitorRun = true
	poolMonitorMu.Unlock()
	defer func() {
		poolMonitorMu.Lock()
		poolMonitorRun = false
		poolMonitorMu.Unlock()
	}()
	poolRefreshAllCredits(false)
	_ = poolMutate(func(st *poolPersistFile) error {
		st.LastCheck = time.Now()
		return nil
	})
	goPoolEnsure("tick")
}

// ensurePoolMonitor starts the monitor goroutine exactly once.
func ensurePoolMonitor() {
	poolMonitorOnce.Do(func() {
		cfg := poolConfigSnapshot()
		go poolMonitorLoop(cfg.CheckInterval)
	})
}

// poolRestoreCooldowns re-arms the routing cooldown map from the persisted pool
// file. auth_index changes on every host restart, so a parked account would
// otherwise be retried immediately after a restart even though we already know
// it is exhausted or rate limited.
func poolRestoreCooldowns() int {
	files, err := poolListAuths()
	if err != nil {
		return 0
	}
	persist := poolLoad()
	now := time.Now()
	restored := 0
	for _, f := range files {
		sa, _, getErr := poolGetAuthBundle(f.AuthIndex)
		if getErr != nil {
			continue
		}
		key := poolKey(sa.Account.UID, f.ID, f.Name)
		rec := persist.Accounts[key]
		if rec == nil || !rec.QuarantineUntil.After(now) {
			continue
		}
		reason := "429"
		if rec.State == poolStateExhausted {
			reason = "credits_exhausted"
		}
		for _, id := range candidateCooldownIDs(&failoverCandidate{AuthID: f.AuthIndex, Auth: sa}) {
			markAuthCooldownReason(id, rec.QuarantineUntil, reason)
		}
		restored++
	}
	if restored > 0 {
		guardLogf("pool: restored %d quarantined account(s) from %s", restored, poolStateFileName)
	}
	return restored
}

// poolRefreshAllCredits refreshes the credit snapshot for every account.
// force bypasses the 45s cache. Failures keep the previous snapshot
// (stale-while-error) and never flip an account to exhausted on their own.
func poolRefreshAllCredits(force bool) map[string]any {
	files, err := poolListAuths()
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	var mu sync.Mutex
	updated, failed := 0, 0
	for _, f := range files {
		f := f
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			sa, phys, getErr := poolGetAuthBundle(f.AuthIndex)
			if getErr != nil {
				mu.Lock()
				failed++
				mu.Unlock()
				return
			}
			_, _, cr, errs := cachedAccountDetails(f.ID, sa, force)
			disabled := phys != nil && phys.Disabled
			if cr != nil {
				poolObserveCredits(f.AuthIndex, f.ID, sa.Account.UID, f.Name, accountRegion(sa), cr, disabled)
				mu.Lock()
				updated++
				mu.Unlock()
				return
			}
			mu.Lock()
			failed++
			mu.Unlock()
			poolObserveCreditsError(f.AuthIndex, sa.Account.UID, f.Name,
				accountRegion(sa), strings.Join(errs, "; "))
		}()
	}
	wg.Wait()
	return map[string]any{"accounts": len(files), "updated": updated, "failed": failed, "when": time.Now().UTC().Format(time.RFC3339)}
}

// poolStatePayload is the JSON body of GET .../pool.
func poolStatePayload() map[string]any {
	cfg := poolConfigSnapshot()
	accounts, available := poolSnapshot()
	persist := poolLoad()
	rows := make([]map[string]any, 0, len(accounts))
	for _, a := range accounts {
		rows = append(rows, map[string]any{
			"key":              a.Key,
			"auth_index":       a.AuthIndex,
			"auth_id":          a.AuthID,
			"name":             a.Name,
			"uid":              a.UID,
			"nickname":         a.Nickname,
			"label":            a.Label,
			"region":           a.Region,
			"state":            a.State,
			"reason":           a.Reason,
			"disabled":         a.Disabled,
			"quarantine_until": rfc3339OrEmpty(a.QuarantineUntil),
			"remain":           a.Remain,
			"used":             a.Used,
			"size":             a.Size,
			"credits_known":    a.CreditsKnown,
			"credits_at":       rfc3339OrEmpty(a.CreditsAt),
			"token_expires_at": a.TokenExpiresAt,
			"can_refresh":      a.CanRefresh,
			"in_flight":        a.InFlight,
			"available":        a.Available,
			"counts_toward":    poolCountsToward(a, cfg),
			"failures":         a.Failures,
			"error":            a.Error,
		})
	}
	return map[string]any{
		"available":      available,
		"min_available":  cfg.MinAvailable,
		"healthy":        available >= cfg.MinAvailable,
		"realm":          cfg.Realm,
		"check_interval": cfg.CheckInterval.String(),
		"quarantine_min": cfg.QuarantineMinutes,
		"accounts":       rows,
		"last_check":     rfc3339OrEmpty(persist.LastCheck),
		"server_time":    time.Now().Format(time.RFC3339),
	}
}

func rfc3339OrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func firstAlias(c *failoverCandidate) string {
	if c == nil {
		return ""
	}
	for _, a := range c.Aliases {
		if strings.TrimSpace(a) != "" {
			return a
		}
	}
	return ""
}

func physName(phys *hostAuthPhysical, fallback string) string {
	if phys != nil && strings.TrimSpace(phys.Name) != "" {
		return phys.Name
	}
	return fallback
}

func failureCount(key string) int {
	rec := poolLoad().Accounts[key]
	if rec == nil {
		return 0
	}
	return rec.Failures
}

// -----------------------------------------------------------------------------
// Configuration (plugins.configs.workbuddy)
// -----------------------------------------------------------------------------

// configurePool parses the pool-related keys out of the plugin config YAML.
// The parser is line-based like configure() in usage_config.go: the CPA host
// hands the plugin its own config subtree as YAML text, and only flat
// "key: value" scalars are used here.
func configurePool(raw []byte) {
	cfg := defaultPoolConfig()
	if yamlText := poolConfigYAML(raw); len(yamlText) > 0 {
		for _, line := range strings.Split(string(yamlText), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, ":") {
				continue
			}
			key, value, _ := strings.Cut(line, ":")
			key = strings.TrimSpace(key)
			value = strings.Trim(strings.TrimSpace(value), "\"'")
			switch key {
			case "min_available_accounts":
				if n, ok := parsePositiveInt(value); ok {
					cfg.MinAvailable = n
				}
			case "pool_realm":
				switch strings.ToLower(value) {
				case poolRealmGlobal, poolRealmAny:
					cfg.Realm = strings.ToLower(value)
				}
			case "pool_check_interval":
				if d, ok := parseDurationValue(value); ok {
					cfg.CheckInterval = d
				}
			case "quarantine_minutes":
				if n, ok := parsePositiveInt(value); ok {
					cfg.QuarantineMinutes = n
				}
			}
		}
	}
	// Environment overrides: containers are easier to tune from env than from
	// a mounted config.yaml.
	if v := strings.TrimSpace(os.Getenv("WB_MIN_AVAILABLE")); v != "" {
		if n, ok := parsePositiveInt(v); ok {
			cfg.MinAvailable = n
		}
	}

	poolCfgMu.Lock()
	prevInterval := poolCfg.CheckInterval
	poolCfg = cfg
	// A runtime toggle applied from the panel must survive a reconfigure
	// triggered by an unrelated settings save (the host re-registers the plugin
	// on every config reload). config_yaml itself wins on a process restart.
	poolCfgMu.Unlock()
	if prevInterval != cfg.CheckInterval {
		guardLogf("pool: check interval changed to %s", cfg.CheckInterval)
	}
	ensurePoolMonitor()
}

func parseBoolDefault(s string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "1", "yes", "on":
		return true
	case "false", "0", "no", "off":
		return false
	}
	return def
}

func parsePositiveInt(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
		if n > 1<<20 {
			return 0, false
		}
	}
	return n, true
}

func parseDurationValue(s string) (time.Duration, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	// Bare integers are seconds (YAML-friendly).
	if n, ok := parsePositiveInt(s); ok && n > 0 {
		return time.Duration(n) * time.Second, true
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return d, true
	}
	return 0, false
}

// poolConfigFields describes the pool settings shown in the CPA config UI.
func poolConfigFields() []pluginapi.ConfigField {
	return []pluginapi.ConfigField{
		{Name: "min_available_accounts", Type: pluginapi.ConfigFieldTypeInteger, Description: "Hard floor of usable Global accounts (not disabled, not quarantined, credits > 0, token valid or refreshable). The plugin fails over inside the pool and logs a shortfall when below this floor (default 2). Env: WB_MIN_AVAILABLE."},
		{Name: "pool_realm", Type: pluginapi.ConfigFieldTypeEnum, EnumValues: []string{poolRealmGlobal, poolRealmAny}, Description: "Which accounts count toward min_available_accounts: global (default, the Intl pool) or any realm."},
		{Name: "pool_check_interval", Type: pluginapi.ConfigFieldTypeString, Description: "Monitor tick for credit refresh + invariant check, e.g. 2m or 120 (default 2m)."},
		{Name: "quarantine_minutes", Type: pluginapi.ConfigFieldTypeInteger, Description: "Default process-local quarantine window for exhausted / 429 accounts when the upstream provides no reset time (default 30)."},
	}
}
