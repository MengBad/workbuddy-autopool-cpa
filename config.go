// Deployment configuration: upstream bases, the OAuth issuer endpoint set,
// shared HTTP client and the login-state janitor. Values are derived from
// the environment and the region, never hardcoded at a call site.
package main

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	providerName  = "workbuddy"
	authFileName  = "workbuddy.json"
	pluginLogoURL = "https://raw.githubusercontent.com/MengBad/workbuddy-autopool-cpa/main/logo.png"
	// CN chat/auth gateway (iss = codebuddy.cn realm).
	upstreamBaseCN = "https://copilot.tencent.com"
	// Global chat/auth gateway (iss = workbuddy.ai realm). APISIX on
	// copilot.tencent.com rejects Global JWTs with 401, so Global traffic uses
	// the .ai/.workbuddy hosts; see resolveGlobalBase for the operator override.
	defaultUpstreamBaseGlobal = "https://www.codebuddy.ai"
	clientUA                  = "CLI/2.63.2 CodeBuddy/2.63.2"
	originReferer             = "https://www.codebuddy.cn"

	// Login OAuth endpoints. The login state must be issued by the realm that
	// owns the account, so there is no single fixed login URL: these paths are
	// combined with the realm base in loginEndpointsForRegion.
	//
	// CN issues on copilot.tencent.com with platform=CLI (unchanged). Intl
	// issues on www.codebuddy.ai with platform=ide — that host answers with an
	// authUrl on its own login page, whereas copilot.tencent.com/login redirects
	// the browser to codebuddy.cn and strands an Intl account mid-flow (#5).
	loginAuthStatePathCN   = "/v2/plugin/auth/state?platform=CLI"
	loginAuthStatePathIntl = "/v2/plugin/auth/state?platform=ide"
	loginAcctPath          = "/v2/plugin/login/account?state="
	loginTokenPath         = "/v2/plugin/auth/token?state="

	// Remaining CN endpoint aliases (chat / models). upstreamBaseCN is the only
	// CN base; Global has its own upstreamBaseGlobal. No "upstreamBase" legacy
	// alias — removed in v0.6.31 dead-code sweep.
	endpointTokenRefresh = upstreamBaseCN + "/v2/plugin/auth/token/refresh"
	endpointChat         = upstreamBaseCN + "/v2/chat/completions"
	endpointModels       = upstreamBaseCN + "/console/enterprises/personal/models"

	loginTTL = 5 * time.Minute
)

// Global base is a var rather than a const: in some regions www.codebuddy.ai
// resolves to 0.0.0.1 (GH #14), which breaks login, chat, model discovery and
// billing at once. WB_UPSTREAM_GLOBAL moves the whole Global surface to the
// realm's other host without touching the credential: both hosts serve the
// same account realm and accept the same bearer token.
var (
	upstreamBaseGlobal  = resolveGlobalBase()
	originRefererGlobal = upstreamBaseGlobal
)

// resolveGlobalBase returns the Intl gateway base, honouring
// WB_UPSTREAM_GLOBAL when it is an http(s) base URL. Invalid values (bad
// scheme, path, query, embedded credentials) are ignored with a warning so a
// typo cannot silently break every Global request.
func resolveGlobalBase() string {
	raw := strings.TrimSpace(os.Getenv("WB_UPSTREAM_GLOBAL"))
	if raw == "" {
		return defaultUpstreamBaseGlobal
	}
	u, err := url.Parse(raw)
	if err != nil {
		fmt.Fprintln(os.Stderr, "[workbuddy] WB_UPSTREAM_GLOBAL ignored: want an http(s) base URL with no path, query or credentials")
		return defaultUpstreamBaseGlobal
	}
	scheme := strings.ToLower(u.Scheme)
	if u.Host == "" || (scheme != "https" && scheme != "http") ||
		u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		// Never echo the value: a pasted URL can carry credentials.
		fmt.Fprintln(os.Stderr, "[workbuddy] WB_UPSTREAM_GLOBAL ignored: want an http(s) base URL with no path, query or credentials")
		return defaultUpstreamBaseGlobal
	}
	return scheme + "://" + u.Host
}

// loginCtx holds the cookie-affined HTTP client for one in-flight login flow.
// CodeBuddy associates the browser login with the state issued at auth/state,
// so we must reuse the same cookie jar across the state request and the polls.
type loginCtx struct {
	client  *http.Client
	expires time.Time
	// endpoints is the realm set that issued this state. Polling must target
	// the same gateway: a state minted by one issuer is meaningless to the
	// other, and login/account is realm-scoped.
	endpoints loginEndpoints
}

// loginBaseCN / loginBaseGlobal mirror upstreamBaseCN / upstreamBaseGlobal for
// the login flow as vars so tests can point it at an httptest server — the same
// override pattern billingBase uses.
var (
	loginBaseCN     = upstreamBaseCN
	loginBaseGlobal = upstreamBaseGlobal
)

// loginEndpoints is one realm's OAuth login endpoint set plus the Origin the
// issuer's gateway expects.
type loginEndpoints struct {
	region    string
	authState string
	authToken string // ?state= is appended by the caller
	loginAcct string
	origin    string
}

// loginEndpointsForRegion resolves the issuer for a login region.
//
// Unknown regions resolve to Intl, never to CN: issuing on the CN host is
// exactly what makes an Intl login impossible, so a missing or malformed region
// must not fall back onto it.
func loginEndpointsForRegion(region string) loginEndpoints {
	if normalizeOAuthRegion(region) == oauthRegionCN {
		return loginEndpoints{
			region:    oauthRegionCN,
			authState: loginBaseCN + loginAuthStatePathCN,
			authToken: loginBaseCN + loginTokenPath,
			loginAcct: loginBaseCN + loginAcctPath,
			origin:    originReferer,
		}
	}
	return loginEndpoints{
		region:    oauthRegionIntl,
		authState: loginBaseGlobal + loginAuthStatePathIntl,
		authToken: loginBaseGlobal + loginTokenPath,
		loginAcct: loginBaseGlobal + loginAcctPath,
		origin:    originRefererGlobal,
	}
}

var (
	loginStates sync.Map // state(string) -> *loginCtx
)

// loginStatesPruneInterval bounds how often the janitor sweeps abandoned
// login states (user started a login but never finished).
const loginStatesPruneInterval = time.Minute

var loginJanitorOnce sync.Once

// startLoginJanitor sweeps login states that were started but never polled.
//
// It is started on the first login instead of from init(), and that is not a
// style preference: a c-shared library must be idle while dlopen is returning.
// The Go runtime schedules the very first goroutine on m0, the thread that
// loaded the library — the host's thread. On darwin/amd64 the runtime keeps the
// current goroutine in one hardcoded TLS slot (0x30(GS); see
// cmd/link/internal/ld/sym.go computeTLSOffset, "Apple has reserved a slot in
// the TLS for Go"). A plugin goroutine left running on the host's thread after
// load therefore makes the host read the plugin's g on its next allocation,
// which surfaces as "bad sweepgen in refill" in the host and takes the whole
// kernel down (golang/go#40131). Deferring the sweep keeps load time free of
// goroutines and timers.
func startLoginJanitor() {
	loginJanitorOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(loginStatesPruneInterval)
			defer ticker.Stop()
			for range ticker.C {
				pruneExpiredLogins(time.Now())
			}
		}()
	})
}

// pruneExpiredLogins drops login states whose TTL has passed. Polls already
// reject expired states, so this only reclaims states that were started and
// then abandoned.
func pruneExpiredLogins(now time.Time) {
	loginStates.Range(func(key, value any) bool {
		if lc, ok := value.(*loginCtx); ok && now.After(lc.expires) {
			loginStates.Delete(key)
		}
		return true
	})
}
