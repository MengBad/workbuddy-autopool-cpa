// Executor: the non-streaming and streaming entry points, the synchronous
// stream collector used by the failover loop, and the post-failover hook.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// -----------------------------------------------------------------------------

func handleExecExecute(raw []byte) ([]byte, error) {
	var req pluginapi.ExecutorRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	pool, routedStorage, err := buildRequestFailoverPool(req.Model, req.AuthID, req.StorageJSON, req.AuthAttributes)
	if err != nil {
		return nil, err
	}
	req.StorageJSON = routedStorage
	if mmPrefixRealm(req.Model) != "" && pool.Primary != nil {
		req.AuthID = pool.Primary.AuthID
	}
	sa, err := parseStored(req.StorageJSON)
	if err != nil {
		return nil, err
	}
	// Resolve oauth-model-alias (e.g. "point/deepseek-v4-flash") back to the
	// real upstream model ID; the upstream rejects unknown alias IDs.
	upstreamModel := resolveUpstreamModel(req.Model, accountRegion(sa), req.AuthAttributes)
	started := time.Now()
	authUID := ""
	if sa.Account.UID != "" {
		authUID = sa.Account.UID
	}
	// CodeBuddy rejects non-stream requests (code 11101), so always stream
	// upstream and fold the chunks into a single chat.completion object.
	// prepareUpstreamBody does forceStream + normalizeTools + rewriteSystem +
	// ensureSystemMessage + rewriteModel in ONE unmarshal/marshal pass.
	body := prepareUpstreamBody(req.Payload, req.OriginalRequest, sa, upstreamModel)
	trace := newCacheTrace(req.Payload, req.OriginalRequest, body, req.Metadata, started)

	// Failover (v0.8.8): when the routed account answers 429 / code 6004, the
	// same request is retried on the next eligible enabled account. One usage
	// record per inbound request (success or final failure); the CPAMP publish
	// follows the same single-line rule.
	cands := pool.ordered(req.StorageJSON)
	var completion []byte
	fin := runFailoverForSession(cands, requestSessionKey(req, body, accountRegion(sa)), func(cand *failoverCandidate) (attemptOutcome, int, string, error) {
		out, err := upstreamAttempt(context.Background(), body, req.Model, cand, trace)
		completion = out // nil on error
		return classifyNonStream(out, err)
	})
	switch fin.Outcome {
	case outcomeSuccess:
		answered := findCandidate(cands, fin.AuthID)
		usageAuthID := fin.AuthID
		usageUID := candUID(answered, authUID)
		publishUsage(req.Model, upstreamModel, usageAuthID, started, usageDetailFromCompletion(completion), false, 0, "")
		recordUsageMap(req.Model, upstreamModel, usageAuthID, usageUID, started, usageMapFromCompletion(completion), false, trace)
		onFailoverSuccess(req.AuthID, fin.AuthID, candRecordID(answered, fin.AuthID), authUID)
		return okEnvelope(pluginapi.ExecutorResponse{Payload: completion})
	default:
		usageAuthID, usageUID := failureIdentity(cands, fin, req.AuthID, authUID)
		publishUsage(req.Model, upstreamModel, usageAuthID, started, usage.Detail{}, true, fin.Status, fin.Body)
		recordUsageMap(req.Model, upstreamModel, usageAuthID, usageUID, started, nil, true, trace)
		// Exhaustion is handled non-destructively by the retry loop, not by
		// lifecycle's Global delete policy.
		if fin.Outcome == outcomeStop && fin.Status > 0 && !isRateLimitResponse(fin.Status, fin.Body) {
			reconcileByUID(usageUID, fin.Status, fin.Body)
		}
		if fin.Err == nil {
			fin.Err = fmt.Errorf("workbuddy request failed")
		}
		return nil, fin.Err
	}
}

// executorStreamRequest wraps the host's executor.execute_stream RPC: the
// ExecutorRequest plus the async stream id the host uses to receive chunks.
type executorStreamRequest struct {
	pluginapi.ExecutorRequest
	StreamID       string `json:"stream_id,omitempty"`
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

func handleExecStream(raw []byte) ([]byte, error) {
	var req executorStreamRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	pool, routedStorage, err := buildRequestFailoverPool(req.Model, req.AuthID, req.StorageJSON, req.AuthAttributes)
	if err != nil {
		return nil, err
	}
	req.StorageJSON = routedStorage
	if mmPrefixRealm(req.Model) != "" && pool.Primary != nil {
		req.AuthID = pool.Primary.AuthID
	}
	sa, err := parseStored(req.StorageJSON)
	if err != nil {
		return nil, err
	}
	upstreamModel := resolveUpstreamModel(req.Model, accountRegion(sa), req.AuthAttributes)
	started := time.Now()
	authUID := ""
	if sa.Account.UID != "" {
		authUID = sa.Account.UID
	}
	// Single-pass JSON rewrite (see handleExecExecute for the non-stream path).
	body := prepareUpstreamBody(req.Payload, req.OriginalRequest, sa, upstreamModel)
	trace := newCacheTrace(req.Payload, req.OriginalRequest, body, req.Metadata, started)

	headers := streamHeaders()
	sseFramed := clientNeedsSSEFrame(req.Metadata)

	// No async stream id → fall back to synchronous chunk collection.
	if req.StreamID == "" {
		cands := pool.ordered(req.StorageJSON)
		collector := &sseUsageCollector{}
		var chunks []pluginapi.ExecutorStreamChunk
		partial := false
		fin := runFailoverForSession(cands, requestSessionKey(req.ExecutorRequest, body, accountRegion(sa)), func(cand *failoverCandidate) (attemptOutcome, int, string, error) {
			var statusCode int
			var err error
			chunks, statusCode, err = collectStreamAttempt(body, cand, sseFramed, collector, trace)
			if err != nil {
				if len(chunks) > 0 {
					// Partial data already collected — the sync path returns it
					// to the host, so do not replay on another account.
					partial = true
					return outcomeStop, statusCode, "", err
				}
				if statusCode == 0 {
					return outcomeStop, 0, "", err
				}
				if outcome := classifyAccountFailure(statusCode, err.Error()); outcome != outcomeStop {
					return outcome, statusCode, err.Error(), err
				}
				return outcomeStop, statusCode, err.Error(), err
			}
			return outcomeSuccess, 0, "", nil
		})
		switch fin.Outcome {
		case outcomeSuccess:
			answered := findCandidate(cands, fin.AuthID)
			usageAuthID := fin.AuthID
			usageUID := candUID(answered, authUID)
			publishUsage(req.Model, upstreamModel, usageAuthID, started, collector.detail(), false, 0, "")
			recordUsageMap(req.Model, upstreamModel, usageAuthID, usageUID, started, collector.lastMap(), false, trace)
			onFailoverSuccess(req.AuthID, fin.AuthID, candRecordID(answered, fin.AuthID), authUID)
			return okEnvelope(streamResponse{Headers: headers, Chunks: chunks})
		default:
			if partial {
				// Partial chunks were already collected before the read error —
				// hand them to the host and let it close the stream.
				answered := findCandidate(cands, fin.AuthID)
				usageAuthID := fin.AuthID
				usageUID := candUID(answered, authUID)
				publishUsage(req.Model, upstreamModel, usageAuthID, started, collector.detail(), true, fin.Status, fin.Body)
				recordUsageMap(req.Model, upstreamModel, usageAuthID, usageUID, started, collector.lastMap(), true, trace)
				return okEnvelope(streamResponse{Headers: headers, Chunks: chunks})
			}
			usageAuthID, usageUID := failureIdentity(cands, fin, req.AuthID, authUID)
			publishUsage(req.Model, upstreamModel, usageAuthID, started, usage.Detail{}, true, fin.Status, fin.Body)
			recordUsageMap(req.Model, upstreamModel, usageAuthID, usageUID, started, collector.lastMap(), true, trace)
			if fin.Outcome == outcomeStop && fin.Status > 0 && !isRateLimitResponse(fin.Status, fin.Body) {
				reconcileByUID(usageUID, fin.Status, fin.Body)
			}
			if fin.Err == nil {
				fin.Err = fmt.Errorf("workbuddy stream request failed")
			}
			return nil, fin.Err
		}
	}

	// Async: return immediately with empty chunks. A goroutine pumps the upstream
	// and emits each chunk via host.stream.emit so the client sees true streaming.
	// Use context.Background() (not nil) so the request can be cancelled when the
	// client disconnects — otherwise the pump keeps reading a dead upstream until
	// sharedHTTPClient's 120s timeout, holding a pool slot the whole time.
	ctx, cancel := context.WithCancel(context.Background())
	cands := pool.ordered(req.StorageJSON)
	go pumpWithFailover(ctx, cancel, req, body, upstreamModel, authUID, started, sseFramed, cands, trace)
	return okEnvelope(streamResponse{Headers: headers})
}

// collectStreamAttempt runs one synchronous upstream stream attempt against
// cand, cleaning chunks through the collector. Mirrors the legacy
// collectUpstreamStream body, parameterized by candidate.
func collectStreamAttempt(body []byte, cand *failoverCandidate, sseFramed bool, collector *sseUsageCollector, traces ...*requestCacheTrace) ([]pluginapi.ExecutorStreamChunk, int, error) {
	httpReq, err := http.NewRequest(http.MethodPost, endpointChatFor(cand.Auth), bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	backendHeaders(httpReq, cand.Auth)
	trace := firstCacheTrace(traces)
	attempt := trace.beginAttempt(httpReq, cand.AuthID)
	// Compliance: route via host.http.do_stream so request-log captures the call.
	stream, statusCode, _, err := hostHTTPDoStream(httpReq)
	trace.finishAttempt(attempt, statusCode)
	if err != nil {
		return nil, 0, fmt.Errorf("http_error: %w", err)
	}
	defer stream.Close()
	reader := newHostStreamReader(stream)
	if statusCode >= 400 {
		errPayload, _ := io.ReadAll(reader)
		return nil, statusCode, &upstreamStatusError{status: statusCode, body: string(errPayload)}
	}
	chunks, errAgg := aggregateSSEWithCollector(reader, sseFramed, collector)
	if errAgg != nil {
		if errors.Is(errAgg, errUpstreamFinishError) {
			// CB-053: nothing has been handed to the host yet on the sync path,
			// so drop the collected chunks and let the failover loop retry once
			// instead of returning a half-built stream.
			return nil, 0, errUpstreamFinishError
		}
		return chunks, statusCode, errAgg
	}
	if len(chunks) == 0 {
		// HTTP 200 but the upstream closed before any data chunk — an
		// upstream/request problem, never an account problem. Returning it as
		// an error here keeps the host from synthesizing a retryable
		// empty_stream and double-billing a second account.
		return nil, statusCode, errEmptyStream
	}
	return chunks, statusCode, nil
}

// onFailoverSuccess refreshes credits + panel selection after a successful
// chat. When the answering account differs from the routed one the active
// panel selection follows it so subsequent scheduler.pick calls stay on the
// healthy account.
// onFailoverSuccess refreshes credits for the account that actually answered.
// It deliberately does NOT record a global "active" account: routing is
// per-session (see session_affinity.go), so one conversation's failover must
// not steer another conversation's next request.
func onFailoverSuccess(routedAuthID, attemptAuthID, attemptRecordID, authUID string) {
	// The answering account's own UID is the correct credits key.
	fb, err := hostAuthGet(attemptAuthID)
	if err == nil && fb != nil && strings.TrimSpace(fb.Account.UID) != "" {
		authUID = fb.Account.UID
	}
	invalidateAccountCredits(attemptAuthID, authUID)
}
