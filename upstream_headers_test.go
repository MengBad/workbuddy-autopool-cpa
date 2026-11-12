package main

import (
	"net/http"
	"strings"
	"testing"
)

// The gateway validates the caller's client identity in addition to the bearer
// token (code 11128). A request that goes out without the realm's headers is
// refused, so these have to be present on every upstream call.
func TestCommonHeaders(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "https://copilot.tencent.com/v2/chat/completions", nil)
	commonHeaders(req)

	if got := req.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
	if req.Header.Get("User-Agent") == "" {
		t.Fatal("User-Agent missing")
	}
	if !strings.Contains(req.Header.Get("User-Agent"), "CodeBuddy") {
		t.Fatalf("User-Agent = %q, want a CodeBuddy identity", req.Header.Get("User-Agent"))
	}
}

func TestEndpointForRealm(t *testing.T) {
	cn := &storedAuth{Auth: storedTokens{Domain: "https://copilot.tencent.com"}}
	intl := &storedAuth{Auth: storedTokens{Domain: "https://www.codebuddy.ai"}}

	if got := endpointChatFor(cn); !strings.HasSuffix(got, "/v2/chat/completions") {
		t.Fatalf("CN chat endpoint = %q", got)
	}
	if got := endpointTokenRefreshFor(cn); !strings.HasSuffix(got, "/v2/plugin/auth/token/refresh") {
		t.Fatalf("CN refresh endpoint = %q", got)
	}
	// The realm decides the host; a Global credential must never be sent to the
	// CN gateway (APISIX answers 401 for a Global JWT).
	if got := endpointChatFor(intl); strings.HasPrefix(got, upstreamBaseCN) {
		t.Fatalf("Global chat endpoint used the CN host: %q", got)
	}
}

// backendHeaders is the full identity set. It must carry the bearer token and
// must not invent an empty Authorization when the credential has no token.
func TestBackendHeadersCarriesAuthorization(t *testing.T) {
	sa := &storedAuth{
		Auth:    storedTokens{Domain: "https://copilot.tencent.com", AccessToken: "tok-123"},
		Account: storedAccount{UID: "u-1"},
	}
	req, _ := http.NewRequest(http.MethodPost, "https://copilot.tencent.com/v2/chat/completions", nil)
	backendHeaders(req, sa)

	if got := req.Header.Get("Authorization"); got != "Bearer tok-123" {
		t.Fatalf("Authorization = %q", got)
	}
	if req.Header.Get("User-Agent") == "" {
		t.Fatal("User-Agent missing")
	}
}

func TestBackendHeadersWithoutToken(t *testing.T) {
	sa := &storedAuth{Auth: storedTokens{Domain: "https://copilot.tencent.com"}}
	req, _ := http.NewRequest(http.MethodPost, "https://copilot.tencent.com/v2/chat/completions", nil)
	backendHeaders(req, sa)

	if got := req.Header.Get("Authorization"); got == "Bearer " {
		t.Fatal("emitted an empty bearer header")
	}
}
