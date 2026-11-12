package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// Both storage shapes are accepted because the plugin writes the nested one
// while CPA-Manager-Plus imports the flat one.
func TestParseStoredNestedShape(t *testing.T) {
	sa, err := parseStored([]byte(`{
		"auth":{"accessToken":"at","refreshToken":"rt","expiresAt":1796903798,"domain":"https://copilot.tencent.com"},
		"account":{"uid":"u-1","enterpriseId":"e-1","nickname":"Mewuy"}
	}`))
	if err != nil {
		t.Fatalf("parseStored: %v", err)
	}
	if sa.Auth.AccessToken != "at" || sa.Auth.RefreshToken != "rt" {
		t.Fatalf("tokens = %+v", sa.Auth)
	}
	if sa.Auth.ExpiresAt != 1796903798 {
		t.Fatalf("expiresAt = %d", sa.Auth.ExpiresAt)
	}
	if sa.Auth.Domain != "https://copilot.tencent.com" {
		t.Fatalf("domain = %q", sa.Auth.Domain)
	}
	if sa.Account.UID != "u-1" || sa.Account.Nickname != "Mewuy" {
		t.Fatalf("account = %+v", sa.Account)
	}
}

func TestParseStoredFlatShape(t *testing.T) {
	sa, err := parseStored([]byte(`{
		"accessToken":"at2","refreshToken":"rt2","expiresAt":123,"domain":"https://www.codebuddy.ai",
		"uid":"u-2","enterpriseId":"e-2","nickname":"Intl"
	}`))
	if err != nil {
		t.Fatalf("parseStored: %v", err)
	}
	if sa.Auth.AccessToken != "at2" || sa.Auth.ExpiresAt != 123 {
		t.Fatalf("tokens = %+v", sa.Auth)
	}
	if sa.Account.UID != "u-2" || sa.Account.EnterpriseID != "e-2" || sa.Account.Nickname != "Intl" {
		t.Fatalf("account = %+v", sa.Account)
	}
}

// rawJSON must keep fields this plugin does not model, or a refresh/import
// round-trip silently drops host-written data.
func TestParseStoredKeepsUnknownFields(t *testing.T) {
	raw := []byte(`{
		"auth":{"accessToken":"at","someFutureField":"keep-me"},
		"account":{"uid":"u"},
		"anotherProviderKey":{"nested":true}
	}`)
	sa, err := parseStored(raw)
	if err != nil {
		t.Fatalf("parseStored: %v", err)
	}
	if len(sa.rawJSON) == 0 {
		t.Fatal("rawJSON is empty")
	}
	if !strings.Contains(string(sa.rawJSON), "keep-me") {
		t.Fatal("rawJSON dropped an unknown auth field")
	}
	if !strings.Contains(string(sa.rawJSON), "anotherProviderKey") {
		t.Fatal("rawJSON dropped an unknown top-level field")
	}
	// The original document must be copied, not aliased: a caller mutating the
	// input slice afterwards must not rewrite what we persisted.
	raw[10] = 'X'
	if !json.Valid(sa.rawJSON) {
		t.Fatal("rawJSON aliased the caller's buffer")
	}
}

func TestParseStoredErrors(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"empty", ``, "empty auth storage"},
		{"not json", `{`, "storage_parse_error"},
		{"missing token", `{"auth":{"refreshToken":"rt"}}`, "missing accessToken"},
		{"missing token flat", `{"uid":"u"}`, "missing accessToken"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseStored([]byte(tc.raw))
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

// The typed structs must round-trip through JSON under the upstream's field
// names, because the same shapes are posted to /v2/plugin/auth/token.
func TestStoredTypesJSONFieldNames(t *testing.T) {
	raw, err := json.Marshal(tokenData{
		AccessToken: "a", RefreshToken: "r", ExpiresIn: 3600,
		RefreshExpiresIn: 7200, Domain: "d",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"accessToken", "refreshToken", "expiresIn", "refreshExpiresIn", "domain"} {
		if !strings.Contains(string(raw), `"`+key+`"`) {
			t.Fatalf("tokenData is missing %q: %s", key, raw)
		}
	}

	raw, err = json.Marshal(accountData{UID: "u", EnterpriseID: "e", Nickname: "n"})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"uid", "enterpriseId", "nickname"} {
		if !strings.Contains(string(raw), `"`+key+`"`) {
			t.Fatalf("accountData is missing %q: %s", key, raw)
		}
	}
}

func TestAPIEnvelopeDecode(t *testing.T) {
	var env apiEnvelope
	if err := json.Unmarshal([]byte(`{"code":0,"msg":"ok","data":{"uid":"u"}}`), &env); err != nil {
		t.Fatal(err)
	}
	if env.Code != 0 || env.Msg != "ok" {
		t.Fatalf("envelope = %+v", env)
	}
	var data struct {
		UID string `json:"uid"`
	}
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatalf("data: %v", err)
	}
	if data.UID != "u" {
		t.Fatalf("uid = %q", data.UID)
	}
}
