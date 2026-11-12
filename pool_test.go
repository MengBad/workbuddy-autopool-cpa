// pool_test.go covers the minimum-availability invariant, the observation
// hooks that feed it, and the auto-registration trigger. Everything here runs
// without a host: hostAuthList / hostAuthGet are stubbed, exactly like the
// existing failover tests do.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// poolFixture points the persisted pool file at a temp dir and restores every
// global the tests touch.
type poolFixture struct {
	dir  string
	cfg  poolConfig
	auth []pluginapi.HostAuthFileEntry
	data map[string]*storedAuth

	// mu guards auth/data: the plugin's own background goroutines (post-register
	// refill, monitor tick) read them through the stubs while a test appends.
	mu sync.Mutex
}

// appendAuth adds one entry to the fake host store (safe for concurrent reads).
func (f *poolFixture) appendAuth(entry pluginapi.HostAuthFileEntry, sa *storedAuth) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth = append(f.auth, entry)
	if sa != nil {
		f.data[entry.AuthIndex] = sa
	}
}

// authEntries / authFor are the concurrency-safe readers used by the stubs.
func (f *poolFixture) authEntries() []pluginapi.HostAuthFileEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]pluginapi.HostAuthFileEntry(nil), f.auth...)
}

func (f *poolFixture) authFor(authIndex string) (*storedAuth, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sa, ok := f.data[authIndex]
	return sa, ok
}

func newPoolFixture(t *testing.T) *poolFixture {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("WORKBUDDY_DATA_DIR", dir)

	prevCfg := poolConfigSnapshot()
	poolCfgMu.Lock()
	poolCfg = defaultPoolConfig()
	poolCfgMu.Unlock()
	poolStateMu.Lock()
	poolState, poolStatePath = nil, ""
	poolStateMu.Unlock()

	f := &poolFixture{dir: dir, data: map[string]*storedAuth{}}
	t.Cleanup(func() {
		// Background invariant goroutines read the seams and the pool state;
		// wait for them BEFORE restoring anything, or the race detector reports
		// a test-only write/read pair.
		poolWaitBackground()
		poolCfgMu.Lock()
		poolCfg = prevCfg
		poolCfgMu.Unlock()
		poolStateMu.Lock()
		poolState, poolStatePath = nil, ""
		poolStateMu.Unlock()
		// Drop cooldowns so one test cannot park an account for the next.
		cooldownMu.Lock()
		cooldowns = map[string]authCooldown{}
		cooldownMu.Unlock()
	})
	return f
}

// add registers one fake account in the fixture and stubs the host bridge.
func (f *poolFixture) add(t *testing.T, uid, domain string, disabled bool, remain int64) *failoverCandidate {
	t.Helper()
	idx := "idx-" + uid
	sa := &storedAuth{
		Auth:    storedTokens{AccessToken: "at-" + uid, RefreshToken: "rt-" + uid, Domain: domain, ExpiresAt: time.Now().Add(24 * time.Hour).Unix()},
		Account: storedAccount{UID: uid, Nickname: "nick-" + uid},
	}
	f.appendAuth(pluginapi.HostAuthFileEntry{
		ID: uid, AuthIndex: idx, Name: "workbuddy-" + uid + ".json", Disabled: disabled,
	}, sa)
	if remain >= 0 {
		accountCache.Store(uid, &accountCacheEntry{
			credits: &creditsSummary{TotalRemain: remain, TotalUsed: 10, TotalSize: 100},
			fetched: time.Now(),
		})
	}
	return &failoverCandidate{AuthID: idx, RecordID: uid, Aliases: []string{"workbuddy-" + uid + ".json"}, Auth: sa}
}

// setSeams installs test stubs under the seam lock so a background goroutine
// (monitor tick / refill) can never observe a half-written func var.
func setSeams(fn func()) {
	poolSeamsMu.Lock()
	defer poolSeamsMu.Unlock()
	fn()
}

// install publishes the fixture to the plugin's host-adapter hooks.
func (f *poolFixture) install(t *testing.T) {
	t.Helper()
	prevList, prevGet := authListSource, authGetSource
	authListMu.Lock()
	authListCache = nil
	authListAt = time.Time{}
	authListMu.Unlock()
	setSeams(func() {
		poolAuthListSource = func() ([]pluginapi.HostAuthFileEntry, error) {
			return f.authEntries(), nil
		}
		poolAuthBundleSource = func(authIndex string) (*storedAuth, *hostAuthPhysical, error) {
			sa, ok := f.authFor(authIndex)
			if !ok {
				return nil, nil, fmt.Errorf("unknown auth %q", authIndex)
			}
			phys := &hostAuthPhysical{AuthIndex: authIndex, Name: "workbuddy-" + sa.Account.UID + ".json"}
			for _, e := range f.authEntries() {
				if e.AuthIndex == authIndex {
					phys.Disabled = e.Disabled
				}
			}
			return sa, phys, nil
		}
	})
	authListSource = func() ([]pluginapi.HostAuthFileEntry, error) {
		return f.authEntries(), nil
	}
	authGetSource = func(authIndex string) (*storedAuth, error) {
		if sa, ok := f.authFor(authIndex); ok {
			return sa, nil
		}
		return nil, fmt.Errorf("unknown auth %q", authIndex)
	}
	t.Cleanup(func() {
		poolWaitBackground()
		authListSource, authGetSource = prevList, prevGet
		setSeams(func() {
			poolAuthListSource = hostAuthList
			poolAuthBundleSource = hostAuthGetBundle
		})
		authListMu.Lock()
		authListCache = nil
		authListAt = time.Time{}
		authListMu.Unlock()
	})
}

func TestPoolSnapshotCountsAvailableGlobalAccounts(t *testing.T) {
	f := newPoolFixture(t)
	f.add(t, "g1", "www.codebuddy.ai", false, 500)
	f.add(t, "g2", "www.workbuddy.ai", false, 0) // exhausted: zero credits
	f.add(t, "c1", "www.codebuddy.cn", false, 500)
	f.add(t, "g3", "www.codebuddy.ai", true, 500) // disabled
	f.install(t)

	available, accounts := poolAvailableCount()
	if available != 1 {
		t.Fatalf("available = %d, want 1 (only g1 counts)", available)
	}
	if len(accounts) != 4 {
		t.Fatalf("accounts = %d, want 4", len(accounts))
	}
	byUID := map[string]*poolAccount{}
	for _, a := range accounts {
		byUID[a.UID] = a
	}
	if !byUID["g1"].Available {
		t.Error("g1 must be available")
	}
	if byUID["g2"].State != poolStateExhausted {
		t.Errorf("g2 state = %q, want %q", byUID["g2"].State, poolStateExhausted)
	}
	if byUID["g3"].Available {
		t.Error("disabled g3 must never be available")
	}
	if byUID["c1"].Region != poolCNRegion {
		t.Errorf("c1 region = %q", byUID["c1"].Region)
	}
}

func TestPoolCountsTowardRespectsRealmScope(t *testing.T) {
	f := newPoolFixture(t)
	f.add(t, "g1", "www.codebuddy.ai", false, 100)
	f.add(t, "c1", "www.codebuddy.cn", false, 100)
	f.install(t)

	global := poolConfigSnapshot()
	if !poolCountsToward(&poolAccount{Region: realmGlobal}, global) {
		t.Fatal("global account must count in the global scope")
	}
	if poolCountsToward(&poolAccount{Region: poolCNRegion}, global) {
		t.Fatal("cn account must not count in the global scope")
	}
	anyScope := global
	anyScope.Realm = poolRealmAny
	if !poolCountsToward(&poolAccount{Region: poolCNRegion}, anyScope) {
		t.Fatal("cn account must count in the any scope")
	}
}

func TestPoolObserveFailureRateLimitCapsCooldown(t *testing.T) {
	f := newPoolFixture(t)
	cand := f.add(t, "g1", "www.codebuddy.ai", false, 500)
	f.install(t)

	body := `{"code":6004,"msg":"usage exceeds frequency limit, reset at 2099-01-01 00:00:00 UTC+8"}`
	markCandidateUnavailable(cand, outcomeRateLimited, body)

	rec := poolLoad().Accounts[poolKey("g1", "g1", "workbuddy-g1.json")]
	if rec == nil || rec.State != poolStateQuarantined {
		t.Fatalf("record = %+v, want quarantined", rec)
	}
	if d := time.Until(rec.QuarantineUntil); d > maxRateLimitCooldown+time.Minute {
		t.Fatalf("429 quarantine = %s, must be capped at %s", d, maxRateLimitCooldown)
	}
}

func TestPoolObserveSuccessClearsQuarantineButNotExhaustion(t *testing.T) {
	f := newPoolFixture(t)
	cand := f.add(t, "g1", "www.codebuddy.ai", false, 500)
	f.install(t)

	markCandidateUnavailable(cand, outcomeRateLimited, `{"code":6004}`)
	resetCandidateCooldown(cand)
	rec := poolLoad().Accounts[poolKey("g1", "g1", "workbuddy-g1.json")]
	if rec == nil || rec.State != poolStateActive {
		t.Fatalf("after success record = %+v, want active", rec)
	}

	markCandidateUnavailable(cand, outcomeAccountExhausted, `{"code":14018}`)
	resetCandidateCooldown(cand)
	rec = poolLoad().Accounts[poolKey("g1", "g1", "workbuddy-g1.json")]
	if rec == nil || rec.State != poolStateExhausted {
		t.Fatalf("a success must not disprove an explicit credit rejection: %+v", rec)
	}
}

func TestPoolObserveCreditsFlipsState(t *testing.T) {
	f := newPoolFixture(t)
	f.add(t, "g1", "www.codebuddy.ai", false, 100)
	f.install(t)

	exhausted := &creditsSummary{TotalRemain: 0, TotalUsed: 100, TotalSize: 100}
	poolObserveCredits("idx-g1", "g1", "g1", "workbuddy-g1.json", realmGlobal, exhausted, false)
	rec := poolLoad().Accounts[poolKey("g1", "g1", "workbuddy-g1.json")]
	if rec == nil || rec.State != poolStateExhausted {
		t.Fatalf("record = %+v, want exhausted", rec)
	}

	recovered := &creditsSummary{TotalRemain: 300, TotalUsed: 100, TotalSize: 400}
	poolObserveCredits("idx-g1", "g1", "g1", "workbuddy-g1.json", realmGlobal, recovered, false)
	rec = poolLoad().Accounts[poolKey("g1", "g1", "workbuddy-g1.json")]
	if rec == nil || rec.State != poolStateActive {
		t.Fatalf("record = %+v, want active after a positive balance", rec)
	}
	if !rec.CreditsKnown || rec.Remain != 300 {
		t.Fatalf("credits snapshot = %+v", rec)
	}
}

func TestPoolPersistSurvivesReload(t *testing.T) {
	f := newPoolFixture(t)
	f.add(t, "g1", "www.codebuddy.ai", false, 100)
	f.install(t)

	markCandidateUnavailable(&failoverCandidate{
		AuthID: "idx-g1", RecordID: "g1", Aliases: []string{"workbuddy-g1.json"},
		Auth: &storedAuth{Auth: storedTokens{Domain: "www.codebuddy.ai"}, Account: storedAccount{UID: "g1"}},
	}, outcomeAccountExhausted, `{"code":14018}`)

	path := filepath.Join(f.dir, poolStateFileName)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("pool file not written: %v", err)
	}
	var doc poolPersistFile
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("pool file not valid JSON: %v", err)
	}
	if doc.Accounts[poolKey("g1", "g1", "")].State != poolStateExhausted {
		t.Fatalf("persisted state = %+v", doc.Accounts)
	}

	// Restart simulation: drop the cache and read again.
	poolStateMu.Lock()
	poolState, poolStatePath = nil, ""
	poolStateMu.Unlock()
	if rec := poolLoad().Accounts[poolKey("g1", "g1", "")]; rec == nil || rec.State != poolStateExhausted {
		t.Fatalf("state did not survive a reload: %+v", rec)
	}
}

func TestPoolRestoreCooldownsAfterRestart(t *testing.T) {
	f := newPoolFixture(t)
	f.add(t, "g1", "www.codebuddy.ai", false, 100)
	f.install(t)

	until := time.Now().Add(18 * time.Minute).UTC().Truncate(time.Second)
	_ = poolMutate(func(st *poolPersistFile) error {
		st.Accounts[poolKey("g1", "g1", "workbuddy-g1.json")] = &poolAccountRecord{
			UID: "g1", State: poolStateExhausted, Reason: "credits_exhausted", QuarantineUntil: until,
		}
		return nil
	})
	cooldownMu.Lock()
	cooldowns = map[string]authCooldown{}
	cooldownMu.Unlock()

	if n := poolRestoreCooldowns(); n != 1 {
		t.Fatalf("restored = %d, want 1", n)
	}
	if !isAuthOnCooldown("idx-g1") {
		t.Fatal("cooldown not re-armed after restart")
	}
	if d := time.Until(authCooldownUntil("idx-g1")); d > 20*time.Minute || d <= 0 {
		t.Fatalf("restored window = %s, want ~18m", d)
	}
}

func TestPoolQuarantineAndRelease(t *testing.T) {
	f := newPoolFixture(t)
	f.add(t, "g1", "www.codebuddy.ai", false, 100)
	f.install(t)

	out, err := poolQuarantine("idx-g1", 5, "manual-test")
	if err != nil {
		t.Fatalf("quarantine: %v", err)
	}
	if out["success"] != true {
		t.Fatalf("quarantine result = %+v", out)
	}
	if available, _ := poolAvailableCount(); available != 0 {
		t.Fatalf("available = %d, want 0 while quarantined", available)
	}
	if _, err := poolRelease("idx-g1"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if available, _ := poolAvailableCount(); available != 1 {
		t.Fatalf("available = %d, want 1 after release", available)
	}
	if isAuthOnCooldown("idx-g1") {
		t.Fatal("release must clear the routing cooldown")
	}
}

func TestPoolConfigParsing(t *testing.T) {
	prev := poolConfigSnapshot()
	t.Cleanup(func() {
		poolCfgMu.Lock()
		poolCfg = prev
		poolCfgMu.Unlock()
	})
	yamlText := "min_available_accounts: 3\npool_realm: any\n" +
		"pool_check_interval: 90\nquarantine_minutes: 45\n"
	// The wire type is []byte, so encoding/json base64-encodes it. Build the
	// request exactly the way the host does.
	raw, err := json.Marshal(struct {
		ConfigYAML []byte `json:"config_yaml"`
	}{ConfigYAML: []byte(yamlText)})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	configurePool(raw)
	cfg := poolConfigSnapshot()
	if cfg.MinAvailable != 3 {
		t.Errorf("min_available = %d, want 3", cfg.MinAvailable)
	}
	if cfg.Realm != poolRealmAny {
		t.Errorf("realm = %q", cfg.Realm)
	}
	if cfg.CheckInterval != 90*time.Second {
		t.Errorf("interval = %s", cfg.CheckInterval)
	}
	if cfg.QuarantineMinutes != 45 {
		t.Errorf("quarantine = %d", cfg.QuarantineMinutes)
	}
}

func TestPoolConfigEnvFallback(t *testing.T) {
	prev := poolConfigSnapshot()
	t.Cleanup(func() {
		poolCfgMu.Lock()
		poolCfg = prev
		poolCfgMu.Unlock()
	})
	t.Setenv("WB_MIN_AVAILABLE", "4")
	configurePool(nil)
	cfg := poolConfigSnapshot()
	if cfg.MinAvailable != 4 {
		t.Errorf("min_available = %d, want 4 from env", cfg.MinAvailable)
	}
}
