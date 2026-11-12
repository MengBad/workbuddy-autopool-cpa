// POST /models/test: one real ~25k-token probe that answers "can this model
// serve me right now, and if not, when?".
package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// probeTargetTokens is the input size the probe aims for. Upstream's practical
// throttling line sits far below the advertised context window; 25k reproduces
// the long-conversation case without paying for a full window.
const probeTargetTokens = 25000

// probeFiller is a short repeated phrase: the probe only needs volume, and a
// repeated sentence tokenizes predictably.
const probeFiller = "The quick brown fox jumps over the lazy dog. "

// TestProbeResult is what the panel renders for one model.
type TestProbeResult struct {
	Model      string `json:"model"`
	OK         bool   `json:"ok"`
	Status     int    `json:"status"`
	Kind       string `json:"kind,omitempty"`
	Message    string `json:"message,omitempty"`
	RecoverAt  string `json:"recover_at,omitempty"`
	ProbeChars int    `json:"probe_chars,omitempty"`
	Credit     string `json:"credit,omitempty"`
	ElapsedMs  int64  `json:"elapsed_ms,omitempty"`
}

// resetAtRe matches the upstream reset wording. Upstream writes the instant in
// the message with an explicit UTC offset; we capture both parts.
var resetAtRe = regexp.MustCompile(
	`(\d{4}-\d{2}-\d{2}[ T]\d{2}:\d{2}:\d{2})\s*(?:UTC|GMT)?\s*([+-]\d{1,2}(?::?\d{2})?)?`)

// parseProbeResetAt extracts the reset instant from an upstream error body.
// Returns the zero time when no parseable instant is present — in that case the
// panel says "上游未给出何时恢复" rather than inventing a countdown.
func parseProbeResetAt(body string) time.Time {
	m := resetAtRe.FindStringSubmatch(body)
	if len(m) < 2 {
		return time.Time{}
	}
	loc := time.UTC
	if len(m) > 2 && m[2] != "" {
		off := strings.ReplaceAll(m[2], ":", "")
		sign := 1
		if strings.HasPrefix(off, "-") {
			sign = -1
			off = off[1:]
		} else {
			off = strings.TrimPrefix(off, "+")
		}
		var hours, mins int
		if len(off) <= 2 {
			fmt.Sscanf(off, "%d", &hours)
		} else {
			fmt.Sscanf(off, "%2d%2d", &hours, &mins)
		}
		loc = time.FixedZone(m[2], sign*(hours*3600+mins*60))
	}
	ts := strings.Replace(m[1], "T", " ", 1)
	t, err := time.ParseInLocation("2006-01-02 15:04:05", ts, loc)
	if err != nil {
		return time.Time{}
	}
	return t
}

// probeBody builds a chat payload of roughly probeTargetTokens input tokens.
func probeBody(model string) []byte {
	// ~4 chars per token for this filler; aim slightly over so the request is
	// unmistakably in the "real conversation" size band.
	want := probeTargetTokens * 4
	var b strings.Builder
	b.Grow(want + 256)
	for b.Len() < want {
		b.WriteString(probeFiller)
	}
	payload := map[string]any{
		"model": model,
		"messages": []map[string]any{
			{"role": "user", "content": b.String()},
		},
		"max_tokens": 8,
		"stream":     true,
	}
	raw, _ := json.Marshal(payload)
	return raw
}

// classifyProbeFailure maps an upstream business code to a stable class the
// panel can render, plus a human message. Unknown codes stay "client" so the
// panel shows the raw upstream text instead of a guess.
func classifyProbeFailure(code, body string) (string, string) {
	lower := strings.ToLower(body)
	switch code {
	case "14018":
		return "credits", "积分已用尽（code 14018）"
	case "6004":
		return "rate_limited", "被限流（code 6004）"
	case "11140":
		return "policy_reject", "服务端按内容策略拒绝（code 11140）"
	case "11128":
		return "channel_reject", "网关渠道校验拒绝（code 11128）"
	case "11158":
		return "plan_gate", "该模型不适用于当前账号类型（code 11158）"
	case "14003":
		return "model_busy", "模型繁忙，与账号和积分无关"
	}
	if strings.Contains(lower, "credits exhausted") || strings.Contains(lower, "insufficient credit") {
		return "credits", "积分已用尽"
	}
	if strings.Contains(lower, "frequency limit") || strings.Contains(lower, "rate limit") {
		return "rate_limited", "被限流"
	}
	if strings.Contains(lower, "request illegal") {
		return "policy_reject", "服务端按内容策略拒绝"
	}
	return "client", truncateRedacted(body, 240)
}

// probeCreditFromSSE pulls the "credit" field out of the trailing SSE frames.
func probeCreditFromSSE(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var frame struct {
			Usage struct {
				Credit any `json:"credit"`
			} `json:"usage"`
			Credit any `json:"credit"`
		}
		if err := json.Unmarshal([]byte(payload), &frame); err != nil {
			continue
		}
		if v := frame.Usage.Credit; v != nil {
			return fmt.Sprintf("%v", v)
		}
		if v := frame.Credit; v != nil {
			return fmt.Sprintf("%v", v)
		}
	}
	return ""
}

// probeBodyString returns the filler text used to build a real-sized probe.
func probeBodyString() string {
	want := probeTargetTokens * 4
	var b strings.Builder
	b.Grow(want)
	for b.Len() < want {
		b.WriteString(probeFiller)
	}
	return b.String()
}
