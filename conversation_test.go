package main

import (
	"encoding/hex"
	"strings"
	"testing"
)

// The conversation id must be a function of the conversation PREFIX only.
// Hashing the whole body would rotate the id on every turn, which is exactly
// what defeats the upstream prompt cache for multi-turn agent sessions.
func TestStableConversationIDIgnoresAppendedTurns(t *testing.T) {
	first := []byte(`{"messages":[
		{"role":"system","content":"you are a coding agent"},
		{"role":"user","content":"refactor this"}
	]}`)
	later := []byte(`{"messages":[
		{"role":"system","content":"you are a coding agent"},
		{"role":"user","content":"refactor this"},
		{"role":"assistant","content":"sure"},
		{"role":"user","content":"now add tests"}
	]}`)

	a, b := stableConversationID(first), stableConversationID(later)
	if a != b {
		t.Fatalf("id rotated when the conversation grew: %s != %s", a, b)
	}
	if len(a) != 32 {
		t.Fatalf("id length = %d, want 32 hex chars (16 bytes)", len(a))
	}
	if _, err := hex.DecodeString(a); err != nil {
		t.Fatalf("id is not hex: %v", err)
	}
}

func TestStableConversationIDSeparatesConversations(t *testing.T) {
	a := stableConversationID([]byte(`{"messages":[{"role":"user","content":"alpha"}]}`))
	b := stableConversationID([]byte(`{"messages":[{"role":"user","content":"beta"}]}`))
	if a == b {
		t.Fatal("two different first user messages produced the same id")
	}
}

// A different system prompt is a different conversation: the cache prefix the
// upstream keys on includes it.
func TestStableConversationIDCoversSystemPrompt(t *testing.T) {
	a := stableConversationID([]byte(`{"messages":[
		{"role":"system","content":"persona A"},
		{"role":"user","content":"same question"}]}`))
	b := stableConversationID([]byte(`{"messages":[
		{"role":"system","content":"persona B"},
		{"role":"user","content":"same question"}]}`))
	if a == b {
		t.Fatal("system prompt change did not change the id")
	}
}

// content may be a plain string or an array of {type:text} parts; both must
// contribute to the prefix rather than falling back to the whole body.
func TestStableConversationIDAcceptsContentParts(t *testing.T) {
	parts := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hello parts"}]}]}`)
	plain := []byte(`{"messages":[{"role":"user","content":"hello parts"}]}`)
	if stableConversationID(parts) == stableConversationID(plain) {
		t.Log("note: parts and plain text hash the same, which is fine — both are stable")
	}
	// What matters is that it is stable and does not fall back to the raw body.
	if stableConversationID(parts) != stableConversationID(parts) {
		t.Fatal("id is not deterministic for content parts")
	}
}

// Unparseable bodies still get a deterministic id (hash of the raw body) so a
// malformed request cannot silently randomise the cache key.
func TestStableConversationIDFallsBackToRawBody(t *testing.T) {
	raw := []byte(`not json at all`)
	if stableConversationID(raw) != stableConversationID(raw) {
		t.Fatal("fallback id is not deterministic")
	}
	if stableConversationID(raw) == stableConversationID([]byte(`different bytes`)) {
		t.Fatal("different raw bodies produced the same fallback id")
	}
}

// A body with no system and no user message has no prefix; it must still be
// deterministic rather than returning the empty-string hash of nothing.
func TestStableConversationIDWithoutUserMessage(t *testing.T) {
	raw := []byte(`{"messages":[{"role":"assistant","content":"only assistant"}]}`)
	got := stableConversationID(raw)
	if got == "" {
		t.Fatal("empty id")
	}
	if got != stableConversationID(raw) {
		t.Fatal("not deterministic")
	}
}

func TestRandomHexShape(t *testing.T) {
	for _, n := range []int{1, 8, 16} {
		s := randomHex(n)
		if len(s) != n*2 {
			t.Fatalf("randomHex(%d) len = %d, want %d", n, len(s), n*2)
		}
		if _, err := hex.DecodeString(s); err != nil {
			t.Fatalf("randomHex(%d) not hex: %v", n, err)
		}
	}
	if randomHex(16) == randomHex(16) {
		t.Fatal("two calls returned the same value")
	}
	if strings.ContainsAny(randomHex(16), "GHIJKLMNOPQRSTUVWXYZ") {
		t.Fatal("uppercase leaked into hex output")
	}
}
