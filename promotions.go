// promotions.go implements upstream promotions (modelPromotions) and per-model
// busy level.
//
// Two independent things live here because both are "此刻的事实" that the panel
// shows next to a model name:
//
//   - promotions: /v3/config returns modelPromotions — discount campaigns with a
//     daily window in an explicit timezone, or a validity range. The badge is a
//     function of NOW: outside its own window it must not be drawn. We never
//     invent a discount; every number (factor, label, colour, hover text) comes
//     from upstream.
//   - busy: POST /v2/chat/busy answers how queued a model is right now. CN only —
//     the international gateway returns 404 for this route. Levels are runtime
//     facts and are never persisted.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// promotionBadge is the drawable part of a promotion badge.
type promotionBadge struct {
	Label      string `json:"label,omitempty"`
	ShortLabel string `json:"short_label,omitempty"`
	Color      string `json:"color,omitempty"`
	Icon       string `json:"icon,omitempty"`
	Display    string `json:"display,omitempty"`
}

// promotionScheduleDaily is one daily window, e.g. 23:00–07:50.
type promotionScheduleDaily struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// promotionSchedule is the time condition of a campaign. Exactly one of the two
// shapes is used: either a daily window (repeats every day in Timezone), or a
// validity range (ValidFrom..ValidUntil).
type promotionSchedule struct {
	Timezone   string                   `json:"timezone,omitempty"`
	Daily      []promotionScheduleDaily `json:"daily,omitempty"`
	ValidFrom  string                   `json:"validFrom,omitempty"`
	ValidUntil string                   `json:"validUntil,omitempty"`
}

// promotionDiscount carries the price effect. Factor is authoritative; the
// other two are display hints from upstream.
type promotionDiscount struct {
	Factor           float64 `json:"factor,omitempty"`
	DiscountedCredit string  `json:"discounted_credits,omitempty"`
	DisplayMode      string  `json:"display_mode,omitempty"`
}

// upstreamPromotion mirrors one entry of data.modelPromotions.
type upstreamPromotion struct {
	ID        string             `json:"id"`
	Kind      string             `json:"kind"`
	Enabled   bool               `json:"enabled"`
	Priority  int                `json:"priority"`
	ModelIDs  []string           `json:"modelIds"`
	Badge     promotionBadge     `json:"badge"`
	Hover     map[string]any     `json:"hover"`
	Schedule  *promotionSchedule `json:"schedule"`
	Discount  *promotionDiscount `json:"discount"`
	Tier      string             `json:"tier"`
	TrackKey  string             `json:"trackKey"`
	LabelZh   string             `json:"labelZh"`
	LabelEn   string             `json:"labelEn"`
	RequiresU string             `json:"requiredUserType"`
}

// modelPromotion is the resolved, panel-facing view of a campaign that is
// ACTIVE at the instant it was computed.
type modelPromotion struct {
	ID            string         `json:"id"`
	Kind          string         `json:"kind"`
	Badge         promotionBadge `json:"badge"`
	HoverZh       string         `json:"hover_zh,omitempty"`
	HoverEn       string         `json:"hover_en,omitempty"`
	Factor        float64        `json:"factor"`
	DiscountKnown bool           `json:"discount_known,omitempty"`
	Active        bool           `json:"active"`
	WindowDesc    string         `json:"window_desc,omitempty"`
	EndsAt        string         `json:"ends_at,omitempty"`
}

// promotionSet is the per-realm cache of upstream promotions.
type promotionSet struct {
	mu      sync.RWMutex
	byRealm map[string][]upstreamPromotion
	at      map[string]time.Time
}

var promotions = &promotionSet{
	byRealm: map[string][]upstreamPromotion{},
	at:      map[string]time.Time{},
}

// promotionCacheTTL matches the model catalog cache: both come from the same
// /v3/config response, so there is no reason to refresh them at different rates.
const promotionCacheTTL = 5 * time.Minute

// storePromotions records the promotions parsed out of one /v3/config response.
func storePromotions(realm string, raw json.RawMessage) {
	if len(raw) == 0 {
		return
	}
	var list []upstreamPromotion
	if err := json.Unmarshal(raw, &list); err != nil {
		guardLogf("promotions: %s decode failed: %v", realm, err)
		return
	}
	promotions.mu.Lock()
	promotions.byRealm[realm] = list
	promotions.at[realm] = time.Now()
	promotions.mu.Unlock()
	guardLogf("promotions: %s stored %d campaign(s)", realm, len(list))
}

// promotionsFor returns the cached campaigns for a realm (may be empty).
func promotionsFor(realm string) []upstreamPromotion {
	promotions.mu.RLock()
	defer promotions.mu.RUnlock()
	return promotions.byRealm[realm]
}

func promotionsFresh(realm string) bool {
	promotions.mu.RLock()
	defer promotions.mu.RUnlock()
	at, ok := promotions.at[realm]
	return ok && time.Since(at) < promotionCacheTTL
}

// parseClock turns "23:00" or "7:50" into minute-of-day.
func parseClock(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return 0, false
	}
	h, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	m, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil || h < 0 || h > 24 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

// promotionActiveAt reports whether a campaign applies at instant now, and the
// human description of its window. An unrecognised/absent schedule means the
// campaign is always on (e.g. "订阅优先" has no time window).
func promotionActiveAt(p upstreamPromotion, now time.Time) (bool, string, time.Time) {
	if !p.Enabled {
		return false, "", time.Time{}
	}
	if p.Schedule == nil {
		return true, "", time.Time{}
	}
	loc := time.UTC
	if tz := strings.TrimSpace(p.Schedule.Timezone); tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
		}
	}
	local := now.In(loc)

	// Validity range wins when present.
	if p.Schedule.ValidFrom != "" || p.Schedule.ValidUntil != "" {
		var from, until time.Time
		if p.Schedule.ValidFrom != "" {
			if t, err := time.Parse(time.RFC3339, p.Schedule.ValidFrom); err == nil {
				from = t
			}
		}
		if p.Schedule.ValidUntil != "" {
			if t, err := time.Parse(time.RFC3339, p.Schedule.ValidUntil); err == nil {
				until = t
			}
		}
		ok := true
		if !from.IsZero() && local.Before(from) {
			ok = false
		}
		if !until.IsZero() && !local.Before(until) {
			ok = false
		}
		desc := ""
		if !from.IsZero() || !until.IsZero() {
			desc = fmt.Sprintf("%s — %s", from.Format("2006-01-02 15:04"), until.Format("2006-01-02 15:04"))
		}
		return ok, desc, until
	}

	// Daily window. A window that crosses midnight (23:00–07:50) matches when
	// the clock is past start OR before end.
	if len(p.Schedule.Daily) == 0 {
		return true, "", time.Time{}
	}
	minutes := local.Hour()*60 + local.Minute()
	for _, d := range p.Schedule.Daily {
		start, okS := parseClock(d.Start)
		end, okE := parseClock(d.End)
		if !okS || !okE {
			continue
		}
		// 0:00–23:59 is upstream's way of saying "all day" — treat it as such
		// instead of relying on the wrap-around comparison (which would also
		// match, but the intent is clearer and 23:59 vs 24:00 off-by-one is a
		// real trap for the badge disappearing in the last minute).
		allDay := start == 0 && (end >= 23*60+59)
		in := false
		if allDay {
			in = true
		} else if start <= end {
			in = minutes >= start && minutes < end
		} else {
			in = minutes >= start || minutes < end
		}
		desc := fmt.Sprintf("%s–%s (%s)", d.Start, d.End, p.Schedule.Timezone)
		if in {
			// When does this window close, in absolute terms?
			closeAt := time.Date(local.Year(), local.Month(), local.Day(),
				end/60, end%60, 0, 0, loc)
			if start > end && minutes >= start {
				closeAt = closeAt.Add(24 * time.Hour)
			}
			return true, desc, closeAt
		}
	}
	return false, "", time.Time{}
}

// activePromotionFor picks the highest-priority ACTIVE campaign for one model.
// Tie-break by ID so the choice is stable across calls.
func activePromotionFor(modelID string, now time.Time) *modelPromotion {
	if strings.TrimSpace(modelID) == "" {
		return nil
	}
	var best *upstreamPromotion
	var bestDesc string
	var bestEnds time.Time
	for i := range promotions.byRealm[realmCN] {
		p := promotions.byRealm[realmCN][i]
		if !containsString(p.ModelIDs, modelID) {
			continue
		}
		ok, desc, ends := promotionActiveAt(p, now)
		if !ok {
			// Discount campaigns also carry a *daytime* companion badge whose
			// window is the complement; both are in the list and the time filter
			// decides which one applies. Nothing to do here.
			continue
		}
		if best == nil || p.Priority > best.Priority ||
			(p.Priority == best.Priority && p.ID < best.ID) {
			cp := p
			best, bestDesc, bestEnds = &cp, desc, ends
		}
	}
	if best == nil {
		return nil
	}
	out := &modelPromotion{
		ID:         best.ID,
		Kind:       best.Kind,
		Badge:      best.Badge,
		HoverZh:    stringFromMap(best.Hover, "textZh"),
		HoverEn:    stringFromMap(best.Hover, "textEn"),
		Active:     true,
		WindowDesc: bestDesc,
	}
	if best.Discount != nil {
		out.Factor = best.Discount.Factor
		out.DiscountKnown = true
	}
	if !bestEnds.IsZero() {
		out.EndsAt = bestEnds.UTC().Format(time.RFC3339)
	}
	return out
}

// effectiveMultiplier applies an active discount to a model's base multiplier
// string ("x0.79"). Returns the base string unchanged when no campaign applies,
// so the caller can always render whatever it gets.
func effectiveMultiplier(base string, promo *modelPromotion) string {
	if promo == nil {
		return base
	}
	// factor == 0 means "free during this campaign" (upstream sends 0 for the
	// free-trial campaigns). That is a real price, not "no discount".
	if promo.Factor == 0 && promo.DiscountKnown {
		return "x0.00"
	}
	if promo.Factor <= 0 || promo.Factor >= 1 {
		return base
	}
	if strings.TrimSpace(base) == "" {
		return base
	}
	// base is "x0.79" (upstream format). Multiply the numeric part.
	num := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(base), "x"), "X")
	v, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return base
	}
	scaled := v * promo.Factor
	// Keep two decimals, matching upstream's own formatting.
	return "x" + strconv.FormatFloat(scaled, 'f', 2, 64)
}

func stringFromMap(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// -----------------------------------------------------------------------------
// Busy probe (CN only)
// -----------------------------------------------------------------------------

// busyLevel is the normalised answer of POST /v2/chat/busy.
type busyLevel string

const (
	busyIdle    busyLevel = "idle"        // nobody queued — green rocket
	busyNormal  busyLevel = "normal"      // nominal load
	busyBusy    busyLevel = "busy"        // upstream's own "busy" word
	busyLight   busyLevel = "light"       // mild congestion
	busyHeavy   busyLevel = "heavy"       // heavy congestion
	busyUnavail busyLevel = "unavailable" // not available now
	busyUnknown busyLevel = "unknown"     // probe gave no answer — draw nothing
)

var (
	busyMu    sync.RWMutex
	busyCache = map[string]busyLevel{} // modelID -> level
	busyAt    time.Time
	// cachedEfforts holds the reasoning levels upstream declared per model.
	// Shared with the busy cache's lock because both are "catalog facts".
	cachedEfforts = map[string][]string{}
)

// busyProbeMaxIDs is upstream's hard cap for one /v2/chat/busy call. Sending
// more returns HTTP 400, so the caller must chunk.
const busyProbeMaxIDs = 50

// busyProbeSupported reports whether the realm has this route at all. The
// international gateway answers 404 for every /v2|/v3 chat/busy variant, so we
// do not even try there — a failing request would only add latency.
func busyProbeSupported(realm string) bool {
	return realm == realmCN
}

// fetchBusyLevels asks upstream for the queue level of the given models and
// updates the runtime cache. Failures are logged and leave the cache as-is: a
// busiest-model badge is decoration, it must never block a catalog refresh.
func fetchBusyLevels(sa *storedAuth, modelIDs []string) int {
	if sa == nil || len(modelIDs) == 0 {
		return 0
	}
	if !busyProbeSupported(accountRegion(sa)) {
		return 0
	}
	updated := 0
	for start := 0; start < len(modelIDs); start += busyProbeMaxIDs {
		end := start + busyProbeMaxIDs
		if end > len(modelIDs) {
			end = len(modelIDs)
		}
		chunk := modelIDs[start:end]
		body, err := upstreamPostJSON(sa, "/v2/chat/busy", map[string]any{"models": chunk})
		if err != nil {
			guardLogf("busy: probe failed (%d models): %v", len(chunk), redactSecrets(err.Error()))
			return updated
		}
		var resp struct {
			Code int `json:"code"`
			Data []struct {
				Model     string `json:"model"`
				BusyLevel string `json:"busy_level"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &resp); err != nil || resp.Code != 0 {
			guardLogf("busy: unusable answer (code=%d err=%v)", resp.Code, err)
			return updated
		}
		busyMu.Lock()
		for _, d := range resp.Data {
			lvl := busyLevel(strings.ToLower(strings.TrimSpace(d.BusyLevel)))
			if lvl == "" {
				lvl = busyUnknown
			}
			busyCache[d.Model] = lvl
			updated++
		}
		busyAt = time.Now()
		busyMu.Unlock()
	}
	return updated
}

// busyLevelFor returns the cached level for a model. Absence means "not told",
// which the panel renders as nothing rather than as "all fine".
func busyLevelFor(modelID string) busyLevel {
	busyMu.RLock()
	defer busyMu.RUnlock()
	if l, ok := busyCache[modelID]; ok {
		return l
	}
	return busyUnknown
}

// busySnapshot returns a copy for the management payload.
func busySnapshot() map[string]string {
	busyMu.RLock()
	defer busyMu.RUnlock()
	out := make(map[string]string, len(busyCache))
	for k, v := range busyCache {
		out[k] = string(v)
	}
	return out
}

func busySnapshotAt() string {
	busyMu.RLock()
	defer busyMu.RUnlock()
	if busyAt.IsZero() {
		return ""
	}
	return busyAt.UTC().Format(time.RFC3339)
}

// upstreamPostJSON performs an authenticated POST against the account's realm
// base and returns the raw body. Kept here (not in billing.go) because the busy
// probe is a chat-channel call, not a billing one.
func upstreamPostJSON(sa *storedAuth, path string, payload any) ([]byte, error) {
	if sa == nil {
		return nil, fmt.Errorf("no auth")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	base := upstreamBaseFor(sa)
	req, err := http.NewRequest(http.MethodPost, base+path, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+sa.Auth.AccessToken)
	req.Header.Set("User-Agent", workBuddyUA)
	req.Header.Set("Origin", originRefererFor(sa))
	req.Header.Set("Referer", originRefererFor(sa)+"/")
	client := sharedHTTPClient()
	client.Timeout = 20 * time.Second
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	buf := make([]byte, 0, 8192)
	tmp := make([]byte, 8192)
	for {
		n, err := resp.Body.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			break
		}
		if len(buf) > 1<<20 {
			break
		}
	}
	if resp.StatusCode >= 400 {
		return buf, fmt.Errorf("http %d: %s", resp.StatusCode, truncate(string(buf), 200))
	}
	return buf, nil
}

// promotionModelIDs lists the model ids mentioned by any cached campaign, so the
// busy probe can be narrowed to models that actually matter. Sorted for a stable
// request body (and therefore a stable upstream cache hit).
func promotionModelIDs() []string {
	seen := map[string]struct{}{}
	for _, p := range promotionsFor(realmCN) {
		for _, id := range p.ModelIDs {
			seen[id] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// refreshBusyLevels probes the queue level for every model the realm's catalog
// knows about. Returns how many rows were updated and a non-fatal error string;
// a failed probe must never block a catalog refresh (the badge is decoration).
func refreshBusyLevels(realm, authID string) (int, string) {
	if !busyProbeSupported(realm) {
		// International gateway has no such route; say so once, not per model.
		return 0, ""
	}
	// authID here is a host record id/filename; resolveFetchTarget is the same
	// indirection every other upstream call uses, and it yields the token.
	target, err := mmFetchTargetFn(authID)
	if err != nil || target == nil || target.Token == "" {
		return 0, "no auth for " + authID + ": " + errStringOf(err)
	}
	sa := &storedAuth{Auth: storedTokens{AccessToken: target.Token, Domain: target.Origin}}
	if strings.Contains(target.Origin, "codebuddy.ai") || strings.Contains(target.Origin, "workbuddy.ai") {
		sa.Auth.Domain = "www.codebuddy.ai"
	} else {
		sa.Auth.Domain = "www.codebuddy.cn"
	}
	ids := make([]string, 0, 64)
	for _, e := range catalogModels(realm) {
		if id := strings.TrimSpace(e.ID); id != "" {
			ids = append(ids, id)
		}
	}
	// Prefer the ids upstream actually publishes in this realm's catalog.
	if models, _, mErr := upstreamModelsCached(authID); mErr == nil {
		ids = ids[:0]
		for _, m := range models {
			ids = append(ids, m.ID)
		}
	}
	if len(ids) == 0 {
		return 0, ""
	}
	n := fetchBusyLevels(sa, ids)
	return n, ""
}

// busyTTL bounds how often the panel's own GET can trigger a busy probe. The
// explicit refresh button bypasses it; a repaint does not.
const busyTTL = 3 * time.Minute

// busyStale reports whether the cached levels are older than busyTTL.
func busyStale() bool {
	busyMu.RLock()
	defer busyMu.RUnlock()
	return busyAt.IsZero() || time.Since(busyAt) > busyTTL
}

func errStringOf(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// catalogModelEfforts returns the reasoning levels upstream declared for a model
// in the CN catalog. The declaration rides along on /v3/config, so it is only
// available after at least one catalog fetch; an empty slice means "no
// declaration known", and callers must not invent a level in that case.
func catalogModelEfforts(modelID string) []string {
	busyMu.RLock()
	efforts := cachedEfforts[modelID]
	busyMu.RUnlock()
	return efforts
}

// recordModelEfforts stores the declared levels for a model.
func recordModelEfforts(modelID string, levels []string) {
	if modelID == "" || len(levels) == 0 {
		return
	}
	busyMu.Lock()
	if cachedEfforts == nil {
		cachedEfforts = map[string][]string{}
	}
	cachedEfforts[modelID] = append([]string(nil), levels...)
	busyMu.Unlock()
}
