package main

import (
	"errors"
	"strings"
	"testing"
)

func collectSSE(t *testing.T, body string) ([]string, error) {
	t.Helper()
	var got []string
	err := scanCompletionSSE(strings.NewReader(body), func(payload string) error {
		got = append(got, payload)
		return nil
	})
	return got, err
}

func TestScanCompletionSSEForwardsChunks(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
	got, err := collectSSE(t, body)
	if err != nil {
		t.Fatalf("scanCompletionSSE: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("forwarded %d frames, want 2: %v", len(got), got)
	}
	if !strings.Contains(got[1], "finish_reason") {
		t.Fatalf("second frame = %q", got[1])
	}
}

// Usage-only frames carry no choices but must still reach the collector, or
// the request is billed with zero tokens.
func TestScanCompletionSSEForwardsUsageOnlyFrame(t *testing.T) {
	body := "data: {\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2}}\n\n" +
		"data: [DONE]\n\n"
	got, err := collectSSE(t, body)
	if err != nil {
		t.Fatalf("scanCompletionSSE: %v", err)
	}
	if len(got) != 1 || !strings.Contains(got[0], "prompt_tokens") {
		t.Fatalf("usage frame dropped: %v", got)
	}
}

// Event names, comments and scalar progress notifications are not completion
// chunks. Passing them through breaks CPA's downstream translators.
func TestScanCompletionSSEDropsNonChunkTraffic(t *testing.T) {
	body := ": this is a comment\n\n" +
		"event: ping\ndata: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n" +
		"event: progress\ndata: 42\n\n" +
		"data: {\"metadata\":{\"trace\":\"abc\"}}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"kept\"}}]}\n\n" +
		"data: [DONE]\n\n"
	got, err := collectSSE(t, body)
	if err != nil {
		t.Fatalf("scanCompletionSSE: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("forwarded %d frames, want only the real chunk: %v", len(got), got)
	}
	if !strings.Contains(got[0], "kept") {
		t.Fatalf("wrong frame survived: %q", got[0])
	}
}

func TestScanCompletionSSEStopsAtDone(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n" +
		"data: [DONE]\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"after-done\"}}]}\n\n"
	got, err := collectSSE(t, body)
	if err != nil {
		t.Fatalf("scanCompletionSSE: %v", err)
	}
	for _, f := range got {
		if strings.Contains(f, "after-done") {
			t.Fatal("a frame after [DONE] was forwarded")
		}
	}
}

// A multiline data: block is one JSON document; it must be compacted into a
// single line before it goes out, because the translators read one line per
// chunk.
func TestScanCompletionSSECompactsMultilineData(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":\ndata: {\"content\":\"multi\"}}]}\n\n" +
		"data: [DONE]\n\n"
	got, err := collectSSE(t, body)
	if err != nil {
		t.Fatalf("scanCompletionSSE: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("forwarded %d frames: %v", len(got), got)
	}
	if strings.ContainsAny(got[0], "\r\n") {
		t.Fatalf("multiline payload was not compacted: %q", got[0])
	}
	if !strings.Contains(got[0], "multi") {
		t.Fatalf("payload lost its content: %q", got[0])
	}
}

func TestScanCompletionSSEErrorEventFails(t *testing.T) {
	body := "event: error\ndata: {\"message\":\"upstream exploded\"}\n\n"
	_, err := collectSSE(t, body)
	if err == nil {
		t.Fatal("an error event did not fail the stream")
	}
	if !strings.Contains(err.Error(), "upstream SSE error") {
		t.Fatalf("error = %v", err)
	}
}

func TestScanCompletionSSEInlineErrorObjectFails(t *testing.T) {
	body := "data: {\"error\":{\"message\":\"boom\"}}\n\n"
	_, err := collectSSE(t, body)
	if err == nil {
		t.Fatal("an inline error object did not fail the stream")
	}
}

// Invalid JSON that is not valid JSON of another type means the stream is
// corrupt; silently dropping it would hide a truncated response.
func TestScanCompletionSSEMalformedJSONFails(t *testing.T) {
	body := "data: {not json\n\n"
	_, err := collectSSE(t, body)
	if err == nil {
		t.Fatal("malformed JSON did not fail the stream")
	}
	if !strings.Contains(err.Error(), "invalid upstream completion SSE JSON") {
		t.Fatalf("error = %v", err)
	}
}

// A valid JSON scalar is an informational payload, not corruption.
func TestScanCompletionSSEValidScalarIsDropped(t *testing.T) {
	body := "data: \"a string payload\"\n\ndata: [DONE]\n\n"
	got, err := collectSSE(t, body)
	if err != nil {
		t.Fatalf("a valid scalar payload failed the stream: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("scalar payload forwarded as a chunk: %v", got)
	}
}

func TestScanCompletionSSEConsumeErrorPropagates(t *testing.T) {
	sentinel := errors.New("sink failed")
	err := scanCompletionSSE(
		strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n"),
		func(string) error { return sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want the consumer's error", err)
	}
}

// The last frame is dispatched even without a trailing blank line, otherwise a
// stream that ends abruptly loses its final chunk.
func TestScanCompletionSSEDispatchesTrailingFrame(t *testing.T) {
	got, err := collectSSE(t, "data: {\"choices\":[{\"delta\":{\"content\":\"tail\"}}]}")
	if err != nil {
		t.Fatalf("scanCompletionSSE: %v", err)
	}
	if len(got) != 1 || !strings.Contains(got[0], "tail") {
		t.Fatalf("trailing frame lost: %v", got)
	}
}

// An event larger than the 4 MiB bound must fail loudly rather than silently
// truncating the response.
func TestScanCompletionSSEEventSizeBound(t *testing.T) {
	big := strings.Repeat("x", 5*1024*1024)
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"" + big + "\"}}]}\n\n"
	_, err := collectSSE(t, body)
	if err == nil {
		t.Fatal("an oversized event did not fail")
	}
	if !strings.Contains(err.Error(), "exceeds") && !strings.Contains(err.Error(), "too long") {
		t.Fatalf("error = %v, want a size complaint", err)
	}
}
