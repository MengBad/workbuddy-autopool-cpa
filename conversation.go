// Conversation identity: a stable per-conversation id derived from the request
// body (session affinity, cache tracing) plus the random-suffix helper.
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// stableConversationID derives a stable per-conversation ID from the request
// body prefix (system + first user message). CodeBuddy caches prompt KV-cache
// per X-Conversation-ID; random IDs force cache write on every request.
// Mirror of the SRouter/InferHub session scheme: hash ONLY the conversation
// prefix (system + first user text), NOT the whole body — the body grows on
// every turn, so hashing it would rotate the conversation id each turn and
// defeat the cache for multi-turn coding-agent sessions.
func stableConversationID(body []byte) string {
	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	prefix := ""
	if err := json.Unmarshal(body, &req); err == nil {
		firstUserDone := false
		for _, m := range req.Messages {
			if m.Role == "system" {
				prefix += string(m.Content) + "\n"
			} else if m.Role == "user" && !firstUserDone {
				// content may be a plain string or an array of {type:text}
				var s string
				if json.Unmarshal(m.Content, &s) == nil {
					prefix += s
				} else {
					prefix += string(m.Content)
				}
				firstUserDone = true
			}
		}
	}
	if prefix == "" {
		prefix = string(body)
	}
	h := sha256.Sum256([]byte(prefix))
	return hex.EncodeToString(h[:16])
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
