package shared

import (
	"bytes"
	"strings"

	"github.com/tidwall/gjson"
)

// Zero-output guard inspection. Ports the CPA >=8.0.72 upstream guard intent
// (issue #7513, helps/gemini_zero_output.go) to the CommandCode plugin: a
// stream that terminated cleanly with an upstream finish/done terminal but
// produced zero VISIBLE output (no text, reasoning, or tool-call events)
// fails as a retryable 502 instead of an empty "successful" reply, so the
// host conductor can fail over to another credential.
//
// Detection is deliberately FAIL-OPEN: an event counts as visible output
// unless it matches a RECOGNIZED bookkeeping shape for the source format. A
// conversion future formats or event kinds this file does not know about can
// therefore only ever suppress the guard (miss a zero-output), never fail a
// legitimate output (false 502). Bookkeeping lists are pinned to the event
// shapes the three adapters actually synthesize or pass through.

// claudeBookkeepingEvents are fully synthetic (converter-generated) or
// upstream-protocol-only frames that never carry user-visible content:
// message_start/content_block_start|stop/message_delta/message_stop/ping.
// content_block_start is excluded as visible even when it opens a text block
// — the actual text always arrives as a content_block_delta.
func claudeEventHasVisibleOutput(data string) bool {
	typ := gjson.Get(data, "type").String()
	switch typ {
	case "message_start", "ping", "content_block_start", "content_block_stop",
		"message_delta", "message_stop", "error":
		return false
	case "content_block_delta":
		d := gjson.Get(data, "delta")
		if strings.TrimSpace(d.Get("text").String()) != "" ||
			strings.TrimSpace(d.Get("thinking").String()) != "" ||
			strings.TrimSpace(d.Get("partial_json").String()) != "" {
			return true
		}
		return false
	default:
		// Unknown Claude event kind: fail open.
		return true
	}
}

// openaiEventHasVisibleOutput inspects a chat-completions chunk (bare JSON).
// Bookkeeping = deltas carrying no content/tool_calls/reasoning (role-echo,
// finish chunk, usage-only chunk).
func openaiEventHasVisibleOutput(data string) bool {
	choices := gjson.Get(data, "choices")
	if !choices.Exists() || !choices.IsArray() {
		// Usage-only or unparsed frames are never visible output; genuine
		// provider error payloads do not reach the converter as events.
		return false
	}
	for _, ch := range choices.Array() {
		d := ch.Get("delta")
		if strings.TrimSpace(d.Get("content").String()) != "" ||
			strings.TrimSpace(d.Get("reasoning_content").String()) != "" ||
			strings.TrimSpace(d.Get("reasoning").String()) != "" {
			return true
		}
		if d.Get("tool_calls").Exists() || d.Get("function_call").Exists() {
			return true
		}
		if d.Get("images").Exists() {
			return true
		}
	}
	return false
}

// responseItemVisible reports whether a Responses output item at ADD time
// carries (promise of) output. Message/reasoning items are shell at add
// time — their content always follows as .delta frames — while call items
// carry the call itself.
func responseItemAtAdd(item gjson.Result) bool {
	switch strings.TrimSpace(item.Get("type").String()) {
	case "function_call", "local_shell_call", "custom_tool_call", "web_search_call", "computer_call", "image_generation_call":
		return true
	}
	return false
}

// responseItemAtDone reports whether a completed item carries output: call
// items carry the call itself, message/reasoning items carry text.
func responseItemAtDone(item gjson.Result) bool {
	switch strings.TrimSpace(item.Get("type").String()) {
	case "function_call", "local_shell_call", "custom_tool_call", "web_search_call", "computer_call", "image_generation_call":
		return true
	case "message", "reasoning":
		for _, part := range item.Get("content").Array() {
			if strings.TrimSpace(part.Get("text").String()) != "" {
				return true
			}
		}
		for _, txt := range item.Get("summary").Array() {
			if strings.TrimSpace(txt.Get("text").String()) != "" {
				return true
			}
		}
		return false
	}
	// Unknown item kind at done time: fail open.
	return true
}

// openaiResponseEventHasVisibleOutput inspects a native Responses SSE frame
// (raw passthrough block's data payload). Bookkeeping = lifecycle/done frames.
// Delta frames with a non-empty delta string are visible; *.delta and item
// frames the deny-list does not recognize fail open (visible).
func openaiResponseEventHasVisibleOutput(data string) bool {
	if strings.TrimSpace(data) == "[DONE]" {
		return false
	}
	typ := strings.TrimSpace(gjson.Get(data, "type").String())
	switch typ {
	case "response.created", "response.queued", "response.in_progress", "response.completed",
		"response.content_part.added", "response.content_part.done",
		"response.output_text.annotation.added", "response.output_text.logprob", "",
		"error", "response.failed", "response.refusal.done":
		return false
	case "response.output_item.added":
		return responseItemAtAdd(gjson.Get(data, "item"))
	case "response.output_item.done":
		return responseItemAtDone(gjson.Get(data, "item"))
	}
	if strings.HasSuffix(typ, ".delta") {
		return strings.TrimSpace(gjson.Get(data, "delta").String()) != ""
	}
	// Unknown Responses event kind: fail open.
	return true
}

// eventDataPayloads extracts the JSON payloads to inspect from one converted
// event. "openai" events are bare JSON; "claude"/"openai-response" events are
// SSE blocks whose data: payloads carry the JSON.
func eventDataPayloads(sourceFormat string, evt []byte) []string {
	if sourceFormat == "openai" {
		trimmed := bytes.TrimSpace(evt)
		if len(trimmed) == 0 {
			return nil
		}
		return []string{string(trimmed)}
	}
	var out []string
	for _, line := range bytes.Split(evt, []byte("\n")) {
		trimmed := bytes.TrimSpace(line)
		if !bytes.HasPrefix(trimmed, []byte("data:")) {
			continue
		}
		payload := strings.TrimSpace(string(bytes.TrimPrefix(trimmed, []byte("data:"))))
		if payload != "" {
			out = append(out, payload)
		}
	}
	if len(out) == 0 {
		// Unknown formats (and any converter emitting bare JSON) carry the
		// payload whole; inspecting it keeps the unknown-format fail-open
		// path meaningful.
		return []string{string(bytes.TrimSpace(evt))}
	}
	return out
}

func eventHasVisibleOutput(sourceFormat string, evt []byte) bool {
	for _, data := range eventDataPayloads(sourceFormat, evt) {
		if data == "[DONE]" {
			continue
		}
		visible := false
		switch sourceFormat {
		case "openai":
			visible = openaiEventHasVisibleOutput(data)
		case "claude":
			visible = claudeEventHasVisibleOutput(data)
		case "openai-response":
			visible = openaiResponseEventHasVisibleOutput(data)
		default:
			// Unknown source format: fail open so the guard can only
			// under-fire, never misfire.
			visible = true
		}
		if visible {
			return true
		}
	}
	return false
}

// StreamEventsHaveVisibleOutput reports whether any converted event in the
// batch carries user-visible output (text, reasoning, tool call). The pump
// accumulates this across the whole stream; a stream that terminates cleanly
// with FinishSeen and never sets it fails as a retryable zero-output 502.
func StreamEventsHaveVisibleOutput(sourceFormat string, events [][]byte) bool {
	for _, evt := range events {
		if len(bytes.TrimSpace(evt)) == 0 {
			continue
		}
		if eventHasVisibleOutput(sourceFormat, evt) {
			return true
		}
	}
	return false
}
