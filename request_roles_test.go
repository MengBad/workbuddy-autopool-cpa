package main

import (
	"encoding/json"
	"testing"
)

func decodeAny(t *testing.T, s string) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("bad fixture: %v", err)
	}
	return v
}

// CPA 8.0.23 rewrites Responses "developer" messages to "user". The plugin has
// to put them back as "system", or an instruction silently becomes a user turn.
func TestRestoreResponsesDeveloperRoles(t *testing.T) {
	original := []byte(`{"input":[
		{"type":"message","role":"developer","content":[{"type":"input_text","text":"be terse"}]},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}
	]}`)
	body := decodeAny(t, `{"messages":[
		{"role":"user","content":[{"type":"text","text":"be terse"}]},
		{"role":"user","content":[{"type":"text","text":"hi"}]}
	]}`)

	restoreResponsesDeveloperRoles(body, original)

	msgs := body["messages"].([]any)
	first := msgs[0].(map[string]any)
	if first["role"] != "system" {
		t.Fatalf("developer message became %q, want system", first["role"])
	}
	second := msgs[1].(map[string]any)
	if second["role"] != "user" {
		t.Fatalf("the real user turn was rewritten to %q", second["role"])
	}
}

// Text is matched, not position: an identical string appearing twice must not
// promote the wrong turn.
func TestRestoreResponsesDeveloperRolesMatchesByText(t *testing.T) {
	original := []byte(`{"input":[
		{"type":"message","role":"developer","content":"same words"},
		{"type":"message","role":"user","content":"other"}
	]}`)
	body := decodeAny(t, `{"messages":[
		{"role":"user","content":"other"},
		{"role":"user","content":"same words"}
	]}`)

	restoreResponsesDeveloperRoles(body, original)

	msgs := body["messages"].([]any)
	if msgs[0].(map[string]any)["role"] != "user" {
		t.Fatal("the first (non-matching) message was rewritten")
	}
	if msgs[1].(map[string]any)["role"] != "system" {
		t.Fatal("the matching message was not promoted to system")
	}
}

// The instructions field becomes messages[0] as a system message; the cursor
// must skip it so the first developer message is not matched against it.
func TestRestoreResponsesDeveloperRolesSkipsInstructionsPrefix(t *testing.T) {
	original := []byte(`{"instructions":"you are a helper","input":[
		{"type":"message","role":"developer","content":"dev rule"}
	]}`)
	body := decodeAny(t, `{"messages":[
		{"role":"system","content":"you are a helper"},
		{"role":"user","content":"dev rule"}
	]}`)

	restoreResponsesDeveloperRoles(body, original)

	msgs := body["messages"].([]any)
	if msgs[0].(map[string]any)["role"] != "system" {
		t.Fatal("the instructions prefix was disturbed")
	}
	if msgs[1].(map[string]any)["role"] != "system" {
		t.Fatal("the developer message was not promoted")
	}
}

// Non-message input items (function calls, reasoning) must be ignored rather
// than treated as text to match.
func TestRestoreResponsesDeveloperRolesIgnoresNonMessageItems(t *testing.T) {
	original := []byte(`{"input":[
		{"type":"function_call","name":"f","arguments":"{}"},
		{"type":"message","role":"user","content":"after a tool call"}
	]}`)
	body := decodeAny(t, `{"messages":[{"role":"user","content":"after a tool call"}]}`)

	restoreResponsesDeveloperRoles(body, original)

	if body["messages"].([]any)[0].(map[string]any)["role"] != "user" {
		t.Fatal("a user message was promoted by a function_call item")
	}
}

func TestRestoreResponsesDeveloperRolesNoops(t *testing.T) {
	cases := []struct {
		name     string
		original string
		body     string
	}{
		{"not json", `{`, `{"messages":[{"role":"user","content":"x"}]}`},
		{"no input", `{"messages":[]}`, `{"messages":[{"role":"user","content":"x"}]}`},
		{"empty input", `{"input":[]}`, `{"messages":[{"role":"user","content":"x"}]}`},
		{"no messages", `{"input":[{"role":"user","content":"x"}]}`, `{}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := decodeAny(t, tc.body)
			restoreResponsesDeveloperRoles(body, []byte(tc.original))
			// Must not panic and must not invent messages.
			if msgs, ok := body["messages"].([]any); ok {
				for _, m := range msgs {
					if mm, ok := m.(map[string]any); ok && mm["role"] == "system" {
						t.Fatalf("no-op case promoted a message: %+v", mm)
					}
				}
			}
		})
	}
}

func TestMessageText(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want []string
	}{
		{"string", "hello", []string{"hello"}},
		{"text part", []any{map[string]any{"type": "text", "text": "a"}}, []string{"a"}},
		{"input_text part", []any{map[string]any{"type": "input_text", "text": "a"}}, []string{"a"}},
		{"output_text part", []any{map[string]any{"type": "output_text", "text": "a"}}, []string{"a"}},
		{"no type part", []any{map[string]any{"text": "a"}}, []string{"a"}},
		{"multi part", []any{
			map[string]any{"type": "text", "text": "a"},
			map[string]any{"type": "text", "text": "b"},
		}, []string{"a", "b"}},
		{"nil", nil, nil},
		{"number", 42, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := messageText(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("messageText = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("messageText = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// A mixed image/file message must yield nil: matching only its text could
// promote a different user turn to an instruction.
func TestMessageTextRejectsNonTextParts(t *testing.T) {
	got := messageText([]any{
		map[string]any{"type": "text", "text": "look at this"},
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": "http://x/y.png"}},
	})
	if got != nil {
		t.Fatalf("messageText = %v, want nil for a mixed message", got)
	}
}

func TestMessageTextRejectsMissingText(t *testing.T) {
	got := messageText([]any{map[string]any{"type": "text"}})
	if got != nil {
		t.Fatalf("messageText = %v, want nil when a part has no text", got)
	}
}
