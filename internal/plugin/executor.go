// Non-stream and stream execution paths (FR-005/FR-006/FR-007, arch §4/§5
// steps 5-8): resolve the public model ID against the catalog snapshot,
// translate the inbound payload to the record's upstream protocol, call
// upstream through the host HTTP callbacks, and translate back to the
// client protocol. Request authentication is selected by CPA.

package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"commandcode-go-cliproxyapi/internal/adapter/chatcompletions"
	"commandcode-go-cliproxyapi/internal/adapter/messages"
	"commandcode-go-cliproxyapi/internal/adapter/responses"
	"commandcode-go-cliproxyapi/internal/adapter/shared"
	"commandcode-go-cliproxyapi/internal/catalog"
	"commandcode-go-cliproxyapi/internal/config"
	"commandcode-go-cliproxyapi/internal/errclass"
)

// executorRequest mirrors rpcExecutorRequest: the SDK embeds
// pluginapi.ExecutorRequest untagged, so its fields marshal under Go field
// names ("Model", "SourceFormat", "OriginalRequest", "Stream"). StreamID is
// the DOWNSTREAM host-allocated id for executor.execute_stream emissions;
// ids returned by DoStream are UPSTREAM and never interchangeable (§4).
type executorRequest struct {
	pluginapi.ExecutorRequest
	StreamID string `json:"stream_id,omitempty"`
}

// resolvedExecution carries everything both execution paths need after
// model/key resolution succeeded.
type resolvedExecution struct {
	cfg config.Config
	rec catalog.ModelRecord
	key string
}

// resolveExecution resolves the requested model against the snapshot and
// extracts the key selected by CPA. A non-nil second return is a ready-made
// failure envelope.
func (m *Manager) resolveExecution(req executorRequest) (*resolvedExecution, []byte) {
	m.mu.RLock()
	cfg, mgr := m.cfg, m.mgr
	m.mu.RUnlock()
	if req.AuthProvider != ProviderID {
		return nil, classEnvelope(&errclass.Error{Class: errclass.ClassAuth, Message: "selected auth provider is not commandcode"})
	}
	key := strings.TrimSpace(req.AuthAttributes["api_key"])
	debugTrace("executor auth model=%s auth_id=%s provider=%s attr_api_key_present=%t attr_count=%d storage_json_bytes=%d", req.Model, req.AuthID, req.AuthProvider, key != "", len(req.AuthAttributes), len(req.StorageJSON))
	if key == "" {
		return nil, classEnvelope(&errclass.Error{Class: errclass.ClassAuth, Message: "selected auth has no api key"})
	}
	var rec catalog.ModelRecord
	var found bool
	if mgr != nil && req.Model != "" {
		rec, found = mgr.Lookup(req.Model)
	}
	if !found {
		return nil, classEnvelope(&errclass.Error{
			Class:      errclass.ClassInvalidModel,
			Message:    "model not in routable catalog",
			StatusCode: http.StatusNotFound,
		})
	}
	return &resolvedExecution{cfg: cfg, rec: rec, key: key}, nil
}

// handleExecute implements executor.execute (non-stream). Stream-flagged
// requests are routed to the stream path instead of rejected.
func (m *Manager) handleExecute(request []byte) ([]byte, error) {
	var req executorRequest
	if err := json.Unmarshal(request, &req); err != nil {
		return ErrEnvelope("invalid_request", "malformed executor request body"), nil
	}
	debugTrace("executor invoked model=%s source_format=%s stream=%t original_body_%s payload_%s", req.Model, req.SourceFormat, req.Stream, debugBodyMeta(req.OriginalRequest), debugBodyMeta(req.Payload))
	if req.Stream {
		return m.executeStream(req)
	}
	res, failEnv := m.resolveExecution(req)
	if res == nil {
		return failEnv, nil
	}
	sessionID, eErr := resolveCommandCodeSessionID(req)
	if eErr != nil {
		return classEnvelope(eErr), nil
	}
	debugTrace("executor session mode=%s source_format=%s x_commandcode_session=%s fallback=%t", "non-stream", req.SourceFormat, sessionID, sessionID == emptyCommandCodeSessionID)
	upstreamBody, eErr := buildUpstreamRequest(res.rec.Protocol, res.rec.UpstreamID, req.SourceFormat, req.OriginalRequest, res.rec.Thinking, res.cfg.ResponsesCompatibility)
	if eErr != nil {
		return classEnvelope(eErr), nil
	}

	url := catalog.JoinUpstreamURL(res.cfg.BaseURL, res.rec.EndpointPath)
	debugTrace("executor resolved public_model=%s upstream_model=%s route=%s url=%s key_count=%d", req.Model, res.rec.UpstreamID, res.rec.Protocol, url, len(res.cfg.APIKeys))
	debugTrace("executor sending non-stream url=%s body_len=%d", url, len(upstreamBody))
	ctx, cancel := context.WithTimeout(context.Background(), res.cfg.RequestTimeout)
	defer cancel()
	resp, err := m.bridge.Do(ctx, pluginapi.HTTPRequest{
		Method:  http.MethodPost,
		URL:     url,
		Headers: upstreamAuthHeaders(res.rec.Protocol, res.key, sessionID, req.Headers, false),
		Body:    upstreamBody,
	})
	if err != nil {
		debugTrace("executor non-stream network error: %v", err)
		return classEnvelope(errclass.FromNetwork(err)), nil
	}
	debugTrace("executor received non-stream status=%d body_len=%d", resp.StatusCode, len(resp.Body))
	if resp.StatusCode >= 400 {
		return classEnvelopeWithRetry(shared.UpstreamStatusError(resp.StatusCode, resp.Body), resp.Headers), nil
	}
	// Parse/envelope guard only; true OOM prevention belongs to the host transport's byte cap.
	if int64(len(resp.Body)) > res.cfg.MaxResponseBytes {
		return classEnvelope(errclass.Translation("response exceeds max-response-bytes")), nil
	}
	converted, eErr := convertNonStream(res.rec.Protocol, req.SourceFormat, resp.StatusCode, resp.Body, responsesRequestContext(req))
	if eErr != nil {
		return classEnvelope(eErr), nil
	}
	return okEnvelope(pluginapi.ExecutorResponse{Payload: converted, Headers: resp.Headers}), nil
}

func buildUpstreamRequest(route catalog.Route, upstreamModel, sourceFormat string, sourceBody []byte, ts *pluginapi.ThinkingSupport, responsesCompatibility ...string) ([]byte, *errclass.Error) {
	switch route {
	case catalog.RouteChatCompletions:
		return chatcompletions.BuildRequest(upstreamModel, sourceFormat, sourceBody, ts, responsesCompatibility...)
	case catalog.RouteMessages:
		return messages.BuildRequest(upstreamModel, sourceFormat, sourceBody, ts)
	case catalog.RouteResponses:
		return responses.BuildRequest(upstreamModel, sourceFormat, sourceBody, ts)
	}
	return nil, errclass.Translation("unsupported route")
}

// upstreamAuthHeaders builds the FR-007 upstream headers. Stream requests
// also mirror the native OpenAI-compat executor's Accept/Cache-Control pair
// (openai_compat_executor.go): OpenAI-style gateways and CDNs key response
// buffering off Accept, so a stream request without "Accept:
// text/event-stream" risks server-side buffering that presents as slow
// time-to-first-byte. Content-Type is always set: the upstream body is JSON
// for every route, and a POST body without it is malformed per RFC 9110.
func upstreamAuthHeaders(route catalog.Route, key, sessionID string, inbound http.Header, stream bool) http.Header {
	var h http.Header
	if route == catalog.RouteMessages {
		h = messages.AuthHeaders(key)
	} else {
		// Chat Completions and Responses endpoints are OpenAI-style bearer.
		h = chatcompletions.AuthHeaders(key)
	}
	h.Set("Content-Type", "application/json")
	if stream {
		h.Set("Accept", "text/event-stream")
		h.Set("Cache-Control", "no-cache")
	}
	h.Set("x-commandcode-session", sessionID)
	// CommandCode uses this opt-in to route only through ZDR-capable upstreams.
	// Forward only the documented enabled value; never relay arbitrary client
	// headers across the plugin trust boundary.
	if inbound != nil && strings.TrimSpace(inbound.Get("x-cmd-zdr")) == "1" {
		h.Set("x-cmd-zdr", "1")
	}
	return h
}

const emptyCommandCodeSessionID = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// resolveCommandCodeSessionID applies the FR-012/AC-H precedence: CPA's canonical
// identity, an explicit inbound session header, then the existing content hash.
func resolveCommandCodeSessionID(req executorRequest) (string, *errclass.Error) {
	if sid, ok := req.Metadata["canonical_session_id"].(string); ok && sid != "" {
		return sid, nil
	}
	for _, name := range []string{
		"X-Session-Affinity",
		"X-Commandcode-Session",
		"X-Session-Id",
		"X-Claude-Code-Session-Id",
		"Session-Id",
	} {
		if sid := req.Headers.Get(name); sid != "" {
			return sid, nil
		}
	}
	return deriveCommandCodeSessionID(req.SourceFormat, req.OriginalRequest)
}

// deriveCommandCodeSessionID hashes the model-visible content of the initial user
// turn before translation (FR-012/AC-H). Metadata is excluded;
// valid requests without a user turn use the fixed empty-input digest.
func deriveCommandCodeSessionID(sourceFormat string, originalRequest []byte) (string, *errclass.Error) {
	var content strings.Builder
	appendParts := func(raw json.RawMessage, target string) *errclass.Error {
		parts, eErr := shared.DecodeStringOrParts(raw, target)
		if eErr != nil {
			return eErr
		}
		for _, part := range parts {
			if part.ImageURL != "" {
				content.WriteString(part.ImageURL)
			} else {
				content.WriteString(part.Text)
			}
		}
		return nil
	}

	switch sourceFormat {
	case "openai":
		var req shared.ChatCompletionsRequest
		if err := json.Unmarshal(originalRequest, &req); err != nil {
			return "", errclass.Translation("malformed openai request JSON: " + err.Error())
		}
		started := false
		for _, msg := range req.Messages {
			if !started {
				if msg.Role == "system" || msg.Role == "developer" {
					continue
				}
				if msg.Role != "user" {
					break
				}
				started = true
			} else if msg.Role != "user" {
				break
			}
			if eErr := appendParts(msg.Content, "/v1/chat/completions"); eErr != nil {
				return "", eErr
			}
		}
	case "claude":
		req, eErr := shared.DecodeClaudeMessages(originalRequest)
		if eErr != nil {
			return "", eErr
		}
		started := false
		for _, msg := range req.Messages {
			if !started {
				if msg.Role != "user" {
					continue
				}
				started = true
			} else if msg.Role != "user" {
				break
			}
			if msg.Content != "" {
				content.WriteString(msg.Content)
			}
			for _, block := range msg.Blocks {
				switch block.Kind {
				case "text":
					content.WriteString(block.Text)
				case "image":
					content.WriteString(block.URL)
				case "tool_result":
					text, eErr := shared.ToolResultText(block.Result, "tool messages carry text only")
					if eErr != nil {
						return "", eErr
					}
					content.WriteString(text)
				}
			}
		}
	case "openai-response":
		var req shared.ResponsesRequest
		if err := json.Unmarshal(originalRequest, &req); err != nil {
			return "", errclass.Translation("malformed openai-response request JSON: " + err.Error())
		}
		items, eErr := req.DecodeInputItems()
		if eErr != nil {
			return "", eErr
		}
		started := false
		for _, item := range items {
			isUserMessage := item.Role == "user" && (item.Type == "message" || item.Type == "")
			if !started {
				if !isUserMessage {
					continue
				}
				started = true
			} else if !isUserMessage {
				break
			}
			if eErr := appendParts(item.Content, "/v1/responses"); eErr != nil {
				return "", eErr
			}
		}
	default:
		return "", shared.UnsupportedFormat(sourceFormat, "CommandCode session derivation")
	}

	digest := sha256.Sum256([]byte(content.String()))
	return hex.EncodeToString(digest[:]), nil
}

// convertNonStream routes one upstream response to its adapter's uniform
// translator: every adapter owns status classification (>=400 → §7
// classified errors), native passthrough, and cross-format conversion for
// all client formats.
func convertNonStream(route catalog.Route, sourceFormat string, status int, body []byte, originalRequest ...[]byte) ([]byte, *errclass.Error) {
	switch route {
	case catalog.RouteChatCompletions:
		return chatcompletions.ConvertNonStreamResponse(sourceFormat, status, body, originalRequest...)
	case catalog.RouteMessages:
		return messages.ConvertNonStreamResponse(sourceFormat, status, body)
	case catalog.RouteResponses:
		return responses.ConvertNonStreamResponse(sourceFormat, status, body)
	}
	return nil, errclass.Translation("unsupported route")
}

// classEnvelope renders a classified failure as the wire error envelope;
// ToEnvelopeError redacts and propagates Retryable/HTTPStatus host-side.
func classEnvelope(e *errclass.Error) []byte {
	return classEnvelopeWithRetry(e, nil)
}

// classEnvelopeWithRetry adds the wire retry hint: RetryAfterSeconds exists
// in the pluginabi contract and the host's conductor times credential
// cooldowns from it; older hosts silently ignore it, so nothing depends on
// its presence. Only the delta-seconds form is parsed (an HTTP-date cannot
// be interpreted without clock skew risk); values are floored to positive
// ints and capped at an hour so a buggy upstream cannot pin a credential.
func classEnvelopeWithRetry(e *errclass.Error, headers http.Header) []byte {
	wire := errclass.ToEnvelopeError(e)
	if headers != nil {
		if secs, err := strconv.Atoi(strings.TrimSpace(headers.Get("Retry-After"))); err == nil && secs > 0 {
			if secs > 3600 {
				secs = 3600
			}
			wire.RetryAfterSeconds = secs
		}
	}
	out, _ := json.Marshal(pluginabi.Envelope{OK: false, Error: &wire})
	return out
}

// streamConverter is the common shape of the three adapters' stream
// converters: feed one upstream SSE chunk, get translated client events.
type streamConverter interface {
	Feed(chunk []byte) (events [][]byte, done bool, eErr *errclass.Error)
}

// Compile-time proof the close-without-terminal Flush seam (F5) picks up
// both converters that can hold a deferred terminal.
var (
	_ interface{ Flush() [][]byte } = (*chatcompletions.StreamConverter)(nil)
	_ interface{ Flush() [][]byte } = (*messages.StreamConverter)(nil)
)

func newStreamConverter(route catalog.Route, sourceFormat string, originalRequest ...[]byte) streamConverter {
	switch route {
	case catalog.RouteMessages:
		return messages.NewStreamConverter(sourceFormat)
	case catalog.RouteResponses:
		return responses.NewStreamConverter(sourceFormat)
	}
	return chatcompletions.NewStreamConverter(sourceFormat, originalRequest...)
}

// responsesRequestContext returns only an authentic inbound Responses request
// for request-local custom-tool provenance. OriginalRequest wins when it is
// valid JSON; Payload is a compatibility fallback only for Responses callers
// and only when it is itself a valid JSON object. The translated upstream body
// is never considered because its function schema has already erased custom
// tool identity.
func responsesRequestContext(req executorRequest) []byte {
	if req.SourceFormat != "openai-response" {
		return nil
	}
	validObject := func(raw []byte) bool {
		if !json.Valid(raw) {
			return false
		}
		var object map[string]json.RawMessage
		return json.Unmarshal(raw, &object) == nil && object != nil
	}
	if validObject(req.OriginalRequest) {
		return req.OriginalRequest
	}
	if validObject(req.Payload) {
		return req.Payload
	}
	return nil
}

// handleExecuteStream implements executor.execute_stream (FR-006, §7).
// It decodes once and delegates to executeStream so a stream-flagged
// request arriving via executor.execute is never parsed twice.
func (m *Manager) handleExecuteStream(request []byte) ([]byte, error) {
	var req executorRequest
	if err := json.Unmarshal(request, &req); err != nil {
		return ErrEnvelope("invalid_request", "malformed executor request body"), nil
	}
	debugTrace("executor stream invoked model=%s source_format=%s stream=%t original_body_%s payload_%s", req.Model, req.SourceFormat, req.Stream, debugBodyMeta(req.OriginalRequest), debugBodyMeta(req.Payload))
	return m.executeStream(req)
}

// executeStream runs the already-decoded stream execution: pre-first-byte errors
// (invalid request, unroutable model, upstream HTTP >=400) return immediately as
// error envelopes so CPA can failover pre-emission.
// On success (upstream HTTP 200 OK), the reading and emitting pump loop runs in
// a background goroutine and executeStream returns okEnvelope immediately so the
// host can start draining chunks to the downstream client without buffer deadlocks.
func (m *Manager) executeStream(req executorRequest) ([]byte, error) {
	streamStart := time.Now()
	res, failEnv := m.resolveExecution(req)
	if res == nil {
		return failEnv, nil
	}
	sessionID, eErr := resolveCommandCodeSessionID(req)
	if eErr != nil {
		return classEnvelope(eErr), nil
	}
	debugTrace("executor session mode=%s source_format=%s x_commandcode_session=%s fallback=%t", "stream", req.SourceFormat, sessionID, sessionID == emptyCommandCodeSessionID)
	upstreamBody, eErr := buildUpstreamRequest(res.rec.Protocol, res.rec.UpstreamID, req.SourceFormat, req.OriginalRequest, res.rec.Thinking, res.cfg.ResponsesCompatibility)
	if eErr != nil {
		return classEnvelope(eErr), nil
	}

	url := catalog.JoinUpstreamURL(res.cfg.BaseURL, res.rec.EndpointPath)
	debugTrace("executor sending stream url=%s body_len=%d", url, len(upstreamBody))
	limits := limitsFromConfig(res.cfg)
	ctx, cancel := context.WithTimeout(context.Background(), limits.first)
	defer cancel()
	st, _, id, err := m.bridge.DoStream(ctx, pluginapi.HTTPRequest{
		Method:  http.MethodPost,
		URL:     url,
		Headers: upstreamAuthHeaders(res.rec.Protocol, res.key, sessionID, req.Headers, true),
		Body:    upstreamBody,
	})
	debugTrace("executor stream DoStream status=%d upstreamID=%s err=%v", st, id, err)
	if err != nil {
		cause := "start_error"
		if errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "timed out") {
			cause = "start_timeout"
		}
		m.logStreamFinished(res.rec.Protocol, req.SourceFormat, cause, streamStart, nil, nil, nil, 0, 0, shared.StreamSnapshot{}, false)
		return classEnvelope(errclass.FromNetwork(err)), nil
	}
	if st >= 400 {
		var body []byte
		if id != "" {
			payload, errMsg, _, readErr := m.bridge.StreamRead(id)
			if readErr == nil {
				if len(payload) > 0 {
					body = payload
				} else if errMsg != "" {
					body = []byte(errMsg)
				}
			}
			_ = m.bridge.StreamClose(id)
		}
		m.logStreamFinished(res.rec.Protocol, req.SourceFormat, "http_status", streamStart, nil, nil, nil, int64(len(body)), 0, shared.StreamSnapshot{}, false)
		return classEnvelope(shared.UpstreamStatusError(st, body)), nil
	}

	downID := req.StreamID
	if m.bridge != nil {
		m.bridge.inFlight.Add(1)
	}
	go func() {
		if m.bridge != nil {
			defer m.bridge.inFlight.Done()
		}
		// Panic containment: the pump consumes hostile upstream data inside
		// the host process — an unrecovered panic here would abort the whole
		// CPA host, not just this request.
		defer m.recoverStreamPump(downID)
		m.pumpStream(downID, id, res, req.SourceFormat, streamStart,
			batchingAllowed(req.SourceFormat, executorRequestPath(req)), responsesRequestContext(req))
	}()
	return okEnvelope(struct{}{}), nil
}

// recoverStreamPump absorbs a panic escaping the stream pump goroutine and
// surfaces it through host.log. pumpStream's own deferred close already
// released both streams before the panic reached this guard.
func (m *Manager) recoverStreamPump(downID string) {
	if r := recover(); r == nil {
		return
	}
	if m.bridge == nil {
		return
	}
	_ = m.bridge.Log("error", "stream pump panicked; downstream streams closed",
		map[string]any{"downstream_id": downID})
}

func (m *Manager) pumpStream(downID, upstreamID string, res *resolvedExecution, sourceFormat string, streamStart time.Time, batch bool, originalRequest ...[]byte) {
	var closeOnce sync.Once
	// closeStreams terminates both streams. detail, when non-nil, is the
	// structured pluginabi.Error for the downstream close: the host bridge
	// gives it precedence over the plain message, so classified failures
	// keep their HTTP status / retry guidance for the client.
	closeStreams := func(downErrMsg string, detail *pluginabi.Error) {
		closeOnce.Do(func() {
			_ = m.bridge.StreamClose(upstreamID)
			_ = m.bridge.StreamCloseDownstreamDetail(downID, downErrMsg, detail)
		})
	}
	defer closeStreams("", nil)

	conv := newStreamConverter(res.rec.Protocol, sourceFormat, originalRequest...)
	limits := limitsFromConfig(res.cfg)
	controller := newStreamController(m.bridge, upstreamID, streamStart, limits)
	controller.firstData(time.Until(streamStart.Add(limits.first)))
	defer controller.stop()
	type snapshotter interface{ StreamSnapshot() shared.StreamSnapshot }
	snapper, _ := conv.(snapshotter)
	var lastSnapshot shared.StreamSnapshot
	var (
		total          int64
		upstreamClosed bool
		convDone       bool
		chunks         int64
	)
	headersAt := time.Now()
	var firstDataAt, lastProgressAt *time.Time
	logged := false
	logDone := func(cause string, eof bool) {
		if logged {
			return
		}
		logged = true
		m.logStreamFinished(res.rec.Protocol, sourceFormat, cause, streamStart, &headersAt, firstDataAt, lastProgressAt, total, chunks, lastSnapshot, eof)
	}
	finishCause := ""
	// max-response-bytes accounting: bufferedBytes tracks the payload sitting
	// in the prefetch channel (depth 1) so the consumer cap check counts
	// upstream bytes it has not processed yet. Without it the depth-1 buffer
	// would let the pump read up to one extra chunk (~32 KiB) past the cap.
	var bufferedBytes atomic.Int64
	// Zero-output guard (port of CPA >=8.0.72 issue #7513): a cleanly
	// terminated stream that never carried visible output fails retryably so
	// the host can fail over instead of delivering an empty reply.
	zeroOutputFail := res.cfg.StreamZeroOutputFail
	sawVisibleOutput := false
	// Prefetch: one reader goroutine keeps draining the upstream via
	// StreamRead into a small buffer while the converter/emitter loop works.
	// The host hand-delivers exactly one 32KiB chunk per stream_read RPC with
	// no queueing between them, so the sequential read→feed→emit loop left
	// the upstream socket un-drained for the whole emit phase of every chunk;
	// a slow downstream client then back-pressured the upstream TCP stream
	// and the remote endpoint throttled delivery. The watchdog contract still
	// works unchanged: controller timers close the upstream stream, which
	// releases a reader parked inside StreamRead, and its terminal result
	// reaches the consumer loop where the cause is acted on.
	type upstreamRead struct {
		payload []byte
		errMsg  string
		closed  bool
		err     error
	}
	readAhead := make(chan upstreamRead, 1)
	consumerDone := make(chan struct{})
	defer close(consumerDone)
	go func() {
		for {
			payload, errMsg, closed, err := m.bridge.StreamRead(upstreamID)
			bufferedBytes.Add(int64(len(payload)))
			// Terminal reads are delivered exactly once; delivery relies on
			// the consumer draining every result before exiting, which holds
			// because terminal results always follow (or trigger) the
			// consumer's own close/return paths below.
			select {
			case readAhead <- upstreamRead{payload: payload, errMsg: errMsg, closed: closed, err: err}:
			case <-consumerDone:
				return
			}
			if closed || err != nil || errMsg != "" {
				return
			}
		}
	}()
	for {
		result := <-readAhead
		payload, readErrMsg, closed, err := result.payload, result.errMsg, result.closed, result.err
		bufferedBytes.Add(-int64(len(result.payload)))
		if len(payload) == 0 && !closed && err == nil && readErrMsg == "" {
			// Defensive: the real host never resolves a stream_read with an
			// empty non-terminal payload, but the reader free-runs now, so a
			// misbehaving host would spin this loop at full speed. Back off
			// instead of burning a core.
			time.Sleep(5 * time.Millisecond)
		}
		debugTrace("executor stream read chunk_len=%d closed=%t readErrMsg=%q err=%v", len(payload), closed, readErrMsg, err)
		upstreamClosed = closed
		if cause := controller.Cause(); cause != "" {
			finishCause = cause
			if cause == "finish_grace" {
				if finalizer, ok := conv.(interface {
					FinalizeStream() ([][]byte, *errclass.Error)
				}); ok {
					events, fErr := finalizer.FinalizeStream()
					if fErr != nil {
						wire := errclass.ToEnvelopeError(fErr)
						closeStreams(wire.Message, &wire)
						logDone("terminal_error", upstreamClosed)
						return
					}
					if emitErr := m.emitAll(downID, sourceFormat, batch, events); emitErr != nil {
						closeStreams(errclass.Redact(emitErr.Error()), nil)
						logDone("emit_error", upstreamClosed)
						return
					}
					logDone("finish_grace", upstreamClosed)
					return
				}
				closeStreams("stream terminal incomplete", nil)
				logDone("terminal_error", upstreamClosed)
				return
			}
			closeStreams(streamCauseError(cause), nil)
			logDone(cause, upstreamClosed)
			return
		}
		if err != nil {
			closeStreams(errclass.Redact(err.Error()), nil)
			logDone("read_error", upstreamClosed)
			return
		}
		if readErrMsg != "" {
			closeStreams(errclass.Redact(readErrMsg), nil)
			logDone("upstream_error", upstreamClosed)
			return
		}
		total += int64(len(payload))
		if len(payload) > 0 {
			chunks++
		}
		if total+bufferedBytes.Load() > res.cfg.MaxResponseBytes {
			closeStreams(errclass.Redact("stream exceeded max-response-bytes"), nil)
			logDone("max_bytes", upstreamClosed)
			return
		}
		events, done, convErr := conv.Feed(payload)
		if !sawVisibleOutput && shared.StreamEventsHaveVisibleOutput(sourceFormat, events) {
			sawVisibleOutput = true
		}
		if snapper != nil {
			snap := snapper.StreamSnapshot()
			if snap.Progress > lastSnapshot.Progress {
				now := time.Now()
				if firstDataAt == nil {
					firstDataAt = &now
				}
				lastProgressAt = &now
				controller.progress()
				if snap.FinishSeen && !lastSnapshot.FinishSeen {
					controller.finish()
				}
				lastSnapshot = snap
			}
		}
		if convErr != nil {
			wire := errclass.ToEnvelopeError(convErr)
			closeStreams(wire.Message, &wire)
			logDone("terminal_error", upstreamClosed)
			return
		}
		if emitErr := m.emitAll(downID, sourceFormat, batch, events); emitErr != nil {
			closeStreams(errclass.Redact(emitErr.Error()), nil)
			logDone("emit_error", upstreamClosed)
			return
		}
		convDone = done
		if convDone || upstreamClosed {
			break
		}
	}
	debugTrace("executor stream loop end convDone=%t upstreamClosed=%t", convDone, upstreamClosed)
	if !convDone && upstreamClosed {
		if flusher, ok := conv.(interface {
			FlushWithError() ([][]byte, *errclass.Error)
		}); ok {
			flushed, flushErr := flusher.FlushWithError()
			if flushErr != nil {
				wire := errclass.ToEnvelopeError(flushErr)
				closeStreams(wire.Message, &wire)
				logDone("truncated", true)
				return
			}
			if emitErr := m.emitAll(downID, sourceFormat, batch, flushed); emitErr != nil {
				closeStreams(errclass.Redact(emitErr.Error()), nil)
				logDone("emit_error", true)
				return
			}
		} else if flusher, ok := conv.(interface{ Flush() [][]byte }); ok {
			flushed := flusher.Flush()
			if emitErr := m.emitAll(downID, sourceFormat, batch, flushed); emitErr != nil {
				closeStreams(errclass.Redact(emitErr.Error()), nil)
				logDone("emit_error", true)
				return
			}
		}
	}
	if !logged && finishCause == "" && convDone && zeroOutputFail &&
		lastSnapshot.FinishSeen && !sawVisibleOutput {
		// Zero-output guard: the upstream asserted its completion terminal
		// but nothing visible was ever converted. Mirrors the CPA >=8.0.72
		// zero-output guard (issue #7513): fail retryably (502) instead of
		// committing an empty success, so the host conductor can rotate to
		// another credential. Only fires before any downstream-visible
		// output existed; partial outputs keep streaming (fail-open
		// inspection cannot hide a legitimate output — unknown frames
		// count as visible).
		wire := errclass.ToEnvelopeError(&errclass.Error{
			Class:      errclass.ClassUpstream,
			Message:    "upstream stream finished with zero visible output",
			StatusCode: 502,
			Retryable:  true,
		})
		closeStreams(wire.Message, &wire)
		logDone("zero_output", upstreamClosed)
		return
	}
	if !logged && finishCause == "" {
		finishCause = "terminal"
		if upstreamClosed {
			finishCause = "eof"
		}
		logDone(finishCause, upstreamClosed)
	}
}

func streamCauseError(cause string) string {
	switch cause {
	case "first_data":
		return "stream first data timeout"
	case "idle":
		return "stream idle timeout"
	case "total":
		return "stream total timeout"
	case "finish_grace":
		return "stream terminal incomplete"
	}
	return "stream truncated"
}

func (m *Manager) logStreamFinished(route catalog.Route, sourceFormat, cause string, start time.Time, headersAt, firstDataAt, lastProgressAt *time.Time, total, chunks int64, snapshot shared.StreamSnapshot, eof bool) {
	if m == nil || m.bridge == nil {
		return
	}
	fields := map[string]any{"route": string(route), "source_format": sourceFormat, "cause": cause, "duration_ms": time.Since(start).Milliseconds(), "bytes": total, "chunks": chunks, "finish_seen": snapshot.FinishSeen, "done_seen": snapshot.DoneSeen, "eof": eof, "headers_ms": nil, "first_data_ms": nil, "last_progress_ms": nil}
	if headersAt != nil {
		fields["headers_ms"] = headersAt.Sub(start).Milliseconds()
	}
	if firstDataAt != nil {
		fields["first_data_ms"] = firstDataAt.Sub(start).Milliseconds()
	}
	if lastProgressAt != nil {
		fields["last_progress_ms"] = lastProgressAt.Sub(start).Milliseconds()
	}
	level := "warn"
	if cause == "terminal" || cause == "eof" || cause == "finish_grace" {
		level = "info"
	}
	_ = m.bridge.Log(level, "commandcode stream finished", fields)
}

// emitAll feeds converted events downstream. When batch is set (gated on the
// host-consumer evidence checked by batchingAllowed), each upstream read's
// events are packed into ONE stream_emit RPC carrying a protocol-framed
// payload (shared.JoinStreamEvents): the host deframer (same-format
// chat-completions) and the handler route framers split frames by blank
// lines, so batching is byte-equivalent to per-event emission while replacing
// N cgo round trips (plus a goroutine and timer each) with one.
//
// When batch is false, every event is its own cgo StreamEmit, preserving the
// pre-optimization contract for consumers that translate one CHUNK per call
// with no deframer (cross-format clients) — each event stays an isolated
// unit. StreamEmit remains deadline-bounded (emitTimeout), so a host that
// stops draining the downstream stream surfaces here as an error; the pump
// loop treats that as a post-first-byte stream-fatal failure and runs
// fail(), whose Once-closer releases both streams.
func (m *Manager) emitAll(downStreamID, sourceFormat string, batch bool, events [][]byte) error {
	if !batch {
		for _, evt := range events {
			if err := m.bridge.StreamEmit(downStreamID, evt); err != nil {
				return err
			}
		}
		return nil
	}
	payload := shared.JoinStreamEvents(sourceFormat, events)
	if len(payload) == 0 {
		return nil
	}
	return m.bridge.StreamEmit(downStreamID, payload)
}

// executorRequestPath extracts the client endpoint path the host stashes in
// executor request metadata (coreexecutor.RequestPathMetadataKey, merged into
// the wire payload by the host's mergeExecutorMetadata). Empty when the host
// did not populate it — callers must treat that as no path evidence available.
func executorRequestPath(req executorRequest) string {
	if req.Metadata == nil {
		return ""
	}
	path, _ := req.Metadata["request_path"].(string)
	return strings.TrimSpace(path)
}

// batchingAllowed gates the batched stream_emit optimization on POSITIVE
// evidence the host will split the joined payload back into events. The join
// is byte-equivalent only when the host deframes (same-format openai route,
// HasStreamResponseTransformer true) or passes chunks through verbatim with
// line-based consumers (same-format claude messages / openai-response
// responses routes). Cross-format clients — gemini (`/v1beta/…`),
// interactions endpoints, any requested format outside the plugin's three
// output formats — make the host translate each CHUNK via FormatTranslator's
// Stream with NO deframer, so a multi-event join would corrupt delivery.
// The host's executor request metadata carries the client endpoint path;
// without that evidence we fall back to per-event emission.
//
// Known coupling (hook plugins): on same-format claude and openai-response
// routes with no native transformer, other plugins' response hooks receive
// whatever chunk the plugin emits. Batched, that is one multi-event payload.
// Same-format openai is unaffected (the host deframes before hooks run).
func batchingAllowed(sourceFormat, requestPath string) bool {
	switch {
	case sourceFormat == "openai" && strings.HasSuffix(requestPath, "/chat/completions"):
		return true
	case sourceFormat == "claude" && strings.HasSuffix(requestPath, "/messages"):
		return true
	case sourceFormat == "openai-response" && strings.HasSuffix(requestPath, "/responses"):
		return true
	}
	return false
}
