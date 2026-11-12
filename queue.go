// Upstream queue rejections (6020 / 6021 / 6022) turned into something an
// operator can act on, and never into a pointless account swap.
package main

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	queueCodeWaiting = "6020"
	queueCodeFull    = "6021"
	queueCodeAtLimit = "6022"
)

// queuePositionRe extracts "你排在第 8777 位，队列共 9021 个请求" style answers.
// Upstream localises the wording, so the numbers are captured by position rather
// than by surrounding prose: the first integer is the position, the second (when
// present) is the queue size.
var queuePositionRe = regexp.MustCompile(`(\d{1,9})\s*(?:位|th|place|position)?[^\d]{0,12}(\d{1,9})?`)

// queueRejection is the parsed shape of a queue rejection.
type queueRejection struct {
	Code     string `json:"code"`
	Position int64  `json:"position,omitempty"`
	Total    int64  `json:"total,omitempty"`
	Message  string `json:"message"`
}

// isQueueRejection reports whether the upstream body is a queue rejection.
func isQueueRejection(body string) bool {
	switch upstreamErrorCode(body) {
	case queueCodeWaiting, queueCodeFull, queueCodeAtLimit:
		return true
	}
	lower := strings.ToLower(body)
	return strings.Contains(lower, "6020") || strings.Contains(lower, "6021") ||
		strings.Contains(lower, "6022") ||
		strings.Contains(lower, "queue is full") ||
		strings.Contains(lower, "in queue")
}

// queueMessageText returns the human-readable part of a rejection with the
// business code removed, so the position regex cannot match the code itself.
func queueMessageText(body, code string) string {
	text := body
	if code != "" {
		text = strings.ReplaceAll(text, `"code":`+code, "")
		text = strings.ReplaceAll(text, `"code": "`+code+`"`, "")
		text = strings.ReplaceAll(text, `"code":"`+code+`"`, "")
	}
	if m := upstreamCodeRe.FindStringSubmatch(text); len(m) == 2 {
		text = strings.ReplaceAll(text, m[0], "")
	}
	return text
}

// parseQueueRejection builds the operator-facing description. When upstream
// gives no position we say so instead of inventing a number.
func parseQueueRejection(body string) queueRejection {
	code := upstreamErrorCode(body)
	out := queueRejection{Code: code}
	// Scan the message only, and only after removing the business code. The code
	// itself is a bare integer in the same body, so scanning the whole body made
	// "6020" read as "you are 6020th in line" — a number the upstream never gave.
	if m := queuePositionRe.FindStringSubmatch(queueMessageText(body, code)); len(m) > 1 {
		if v, ok := parsePositiveInt64(m[1]); ok {
			out.Position = v
		}
		if len(m) > 2 && m[2] != "" {
			if v, ok := parsePositiveInt64(m[2]); ok {
				out.Total = v
			}
		}
	}
	switch code {
	case queueCodeFull:
		out.Message = "模型队列已满，请稍后重新发送这条消息，或换一个模型。"
	case queueCodeAtLimit:
		out.Message = "模型已达并发上限，请稍后重新发送这条消息，或换一个模型。"
	default:
		if out.Position > 0 && out.Total > 0 {
			out.Message = fmt.Sprintf("模型排队中：你排在第 %d 位，队列共 %d 个请求。请稍后重新发送这条消息，或换一个模型。",
				out.Position, out.Total)
		} else if out.Position > 0 {
			out.Message = fmt.Sprintf("模型排队中：你排在第 %d 位。请稍后重新发送这条消息，或换一个模型。", out.Position)
		} else {
			out.Message = "模型排队中（上游未给位次）。请稍后重新发送这条消息，或换一个模型。"
		}
	}
	return out
}

func parsePositiveInt64(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	var n int64
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int64(r-'0')
		if n > 1<<40 {
			return 0, false
		}
	}
	return n, true
}
