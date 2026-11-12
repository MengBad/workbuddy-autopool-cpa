// Upstream identity: the client-identity headers the gateway validates in
// addition to the bearer token, resolved per realm.
package main

import (
	"bytes"
	"io"
	"net/http"
)

func commonHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Origin", originReferer)
	req.Header.Set("Referer", originReferer+"/")
	req.Header.Set("User-Agent", clientUA)
}

// originRefererFor returns the Origin/Referer base URL appropriate for the
// account's domain. Global accounts use https://www.workbuddy.ai; CN (and
// legacy auth files with empty domain) use the default https://www.codebuddy.cn.
func originRefererFor(sa *storedAuth) string {
	if sa != nil && isGlobalDomain(sa.Auth.Domain) {
		return originRefererGlobal
	}
	return originReferer
}

// upstreamBaseFor returns the chat/auth API host for the account realm.
// Global JWT iss is workbuddy.ai — those tokens only work on www.workbuddy.ai.
// CN tokens work on copilot.tencent.com. Mixing them yields APISIX 401.
func upstreamBaseFor(sa *storedAuth) string {
	if sa != nil && isGlobalDomain(sa.Auth.Domain) {
		return upstreamBaseGlobal
	}
	return upstreamBaseCN
}

func endpointChatFor(sa *storedAuth) string {
	return upstreamBaseFor(sa) + "/v2/chat/completions"
}

func endpointTokenRefreshFor(sa *storedAuth) string {
	return upstreamBaseFor(sa) + "/v2/plugin/auth/token/refresh"
}

// backendHeaders applies auth-derived headers to a chat completion request.
// Empty fields are signalled via the X-No-* convention used by CodeBuddy.
func backendHeaders(req *http.Request, sa *storedAuth) {
	commonHeaders(req)
	if sa.Auth.AccessToken != "" {
		req.Header.Set("Authorization", "Bearer "+sa.Auth.AccessToken)
	} else {
		req.Header.Set("X-No-Authorization", "1")
	}
	if sa.Account.UID != "" {
		req.Header.Set("X-User-Id", sa.Account.UID)
	} else {
		req.Header.Set("X-No-User-Id", "1")
	}
	if sa.Account.EnterpriseID != "" {
		req.Header.Set("X-Enterprise-Id", sa.Account.EnterpriseID)
	} else {
		req.Header.Set("X-No-Enterprise-Id", "1")
	}
	// SECURITY: do NOT send X-Refresh-Token on chat completions. refresh_token is
	// a long-lived credential that can mint new access_tokens; it only belongs on
	// the refresh endpoint (handleRefreshAuth). Sending it to chat upstream leaks
	// the credential into upstream request logs on every chat call.
	if sa.Auth.Domain != "" {
		req.Header.Set("X-Domain", sa.Auth.Domain)
	} else {
		req.Header.Set("X-No-Department-Info", "1")
	}
	// Client identity the upstream validates: realm-aware UA (IDE for Intl, CLI
	// for CN), X-Product, X-IDE-Type/X-IDE-Name, x-codebuddy-request and the
	// optional device token. See client_identity.go for why this matters (11128).
	applyIdentityHeaders(req, sa)

	// Stable conversation ID enables CodeBuddy prompt cache (KV cache hit).
	// Random per-request IDs cause cache write on every request and zero reads.
	// Derive from request body prefix; per-request sub-IDs stay random, and the
	// tracing family is stamped alongside it.
	cid := ""
	if req.Body != nil {
		if b, err := io.ReadAll(req.Body); err == nil && len(b) > 0 {
			cid = stableConversationID(b)
			req.Body = io.NopCloser(bytes.NewReader(b))
		}
	}
	applyConversationHeaders(req, cid)
	// Override Origin/Referer for Global accounts so the upstream doesn't
	// reject the request as cross-origin.
	origin := originRefererFor(sa)
	req.Header.Set("Origin", origin)
	req.Header.Set("Referer", origin+"/")
}
