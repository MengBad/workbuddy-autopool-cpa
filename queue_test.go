package main

import (
	"strings"
	"testing"
)

func TestIsQueueRejectionByCode(t *testing.T) {
	for _, code := range []string{queueCodeWaiting, queueCodeFull, queueCodeAtLimit} {
		body := `{"code":` + code + `,"msg":"queue"}`
		if !isQueueRejection(body) {
			t.Fatalf("code %s not recognised as a queue rejection", code)
		}
	}
}

// The plain-code path exists because some gateway errors arrive as bare text
// rather than a JSON envelope.
func TestIsQueueRejectionByPlainText(t *testing.T) {
	for _, body := range []string{
		"error 6020 while dispatching",
		"queue is full, try later",
		"you are IN QUEUE number 412",
	} {
		if !isQueueRejection(body) {
			t.Fatalf("%q not recognised as a queue rejection", body)
		}
	}
}

func TestIsQueueRejectionRejectsOtherErrors(t *testing.T) {
	for _, body := range []string{
		`{"code":14018,"msg":"credits exhausted"}`,
		`{"code":6004,"msg":"rate limited"}`,
		"",
		"some unrelated upstream failure",
	} {
		if isQueueRejection(body) {
			t.Fatalf("%q wrongly treated as a queue rejection", body)
		}
	}
}

// Upstream localises the wording, so the numbers are captured by position. The
// operator-facing message must quote both numbers rather than paraphrase them.
func TestParseQueueRejectionChinesePosition(t *testing.T) {
	got := parseQueueRejection(`{"code":6020,"msg":"模型排队中：你排在第 8777 位，队列共 9021 个请求"}`)
	if got.Code != queueCodeWaiting {
		t.Fatalf("code = %q", got.Code)
	}
	if got.Position != 8777 || got.Total != 9021 {
		t.Fatalf("position/total = %d/%d, want 8777/9021", got.Position, got.Total)
	}
	if !strings.Contains(got.Message, "8777") || !strings.Contains(got.Message, "9021") {
		t.Fatalf("message lost the numbers: %q", got.Message)
	}
}

func TestParseQueueRejectionPositionOnly(t *testing.T) {
	got := parseQueueRejection(`{"code":6020,"msg":"queued at position 42"}`)
	if got.Position != 42 {
		t.Fatalf("position = %d, want 42", got.Position)
	}
	if strings.Contains(got.Message, "队列共") {
		t.Fatalf("invented a queue size: %q", got.Message)
	}
}

// "no position given" must be said out loud. Inventing a number here would be
// worse than the upstream's own vagueness.
func TestParseQueueRejectionWithoutPosition(t *testing.T) {
	got := parseQueueRejection(`{"code":6020,"msg":"queued"}`)
	if got.Position != 0 || got.Total != 0 {
		t.Fatalf("invented a position: %+v", got)
	}
	if !strings.Contains(got.Message, "上游未给位次") {
		t.Fatalf("message = %q, want it to admit the missing position", got.Message)
	}
}

// 6021/6022 are about the model, not your place in line, so they must not be
// rendered as a position message.
func TestParseQueueRejectionFullAndAtLimit(t *testing.T) {
	full := parseQueueRejection(`{"code":6021,"msg":"queue full"}`)
	if !strings.Contains(full.Message, "队列已满") {
		t.Fatalf("6021 message = %q", full.Message)
	}
	limit := parseQueueRejection(`{"code":6022,"msg":"at capacity"}`)
	if !strings.Contains(limit.Message, "并发上限") {
		t.Fatalf("6022 message = %q", limit.Message)
	}
}

func TestParsePositiveInt64(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"0", 0, true},
		{"42", 42, true},
		{" 42 ", 42, true},
		{"8777", 8777, true},
		{"", 0, false},
		{"-1", 0, false},
		{"4.2", 0, false},
		{"abc", 0, false},
		{"999999999999999", 0, false}, // beyond the 1<<40 guard
	}
	for _, tc := range cases {
		got, ok := parsePositiveInt64(tc.in)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Fatalf("parsePositiveInt64(%q) = (%d, %v), want (%d, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}
