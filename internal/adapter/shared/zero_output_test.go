package shared

import "testing"

func TestStreamEventsHaveVisibleOutputOpenAI(t *testing.T) {
	visible := [][]byte{
		[]byte(`{"id":"1","choices":[{"delta":{"content":"hi"}}]}`),
		[]byte(`{"id":"2","choices":[{"delta":{"tool_calls":[{"id":"tc","function":{"name":"f"}}]}}]}`),
		[]byte(`{"id":"3","choices":[{"delta":{"reasoning_content":"think"}}]}`),
	}
	for i, evt := range visible {
		if !StreamEventsHaveVisibleOutput("openai", [][]byte{evt}) {
			t.Fatalf("event %d carries visible output but guard missed it", i)
		}
	}
	bookkeeping := [][]byte{
		[]byte(`{"id":"1","choices":[{"delta":{"role":"assistant"},"index":0}]}`),
		[]byte(`{"id":"1","choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"total_tokens":3}}`),
	}
	for i, evt := range bookkeeping {
		if StreamEventsHaveVisibleOutput("openai", [][]byte{evt}) {
			t.Fatalf("bookkeeping event %d wrongly counted visible: %s", i, evt)
		}
	}
}

func TestStreamEventsHaveVisibleOutputFailOpen(t *testing.T) {
	// Fail-open: unrecognized event shapes count as visible so the guard can
	// never misfire on future converter additions.
	if !StreamEventsHaveVisibleOutput("mystery-format", [][]byte{[]byte(`{"whatever":true}`)}) {
		t.Fatal("unknown format must fail open (visible)")
	}
	if !StreamEventsHaveVisibleOutput("claude", [][]byte{[]byte("event: unknown_kind\ndata: {\"type\":\"unknown_kind\"}\n\n")}) {
		t.Fatal("unknown claude event must fail open (visible)")
	}
}

func TestStreamEventsHaveVisibleOutputClaude(t *testing.T) {
	visible := []string{
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"x\\\"\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"hmm\"}}\n\n",
	}
	for i, evt := range visible {
		if !StreamEventsHaveVisibleOutput("claude", [][]byte{[]byte(evt)}) {
			t.Fatalf("claude event %d carries visible output but guard missed it", i)
		}
	}
	bookkeeping := []string{
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"\"}}\n\n",
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\"}\n\n",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
		"event: ping\ndata: {\"type\":\"ping\"}\n\n",
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n",
	}
	for i, evt := range bookkeeping {
		if StreamEventsHaveVisibleOutput("claude", [][]byte{[]byte(evt)}) {
			t.Fatalf("claude bookkeeping %d wrongly counted visible: %s", i, evt)
		}
	}
}

func TestStreamEventsHaveVisibleOutputResponses(t *testing.T) {
	visible := []string{
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n",
		"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\",\"name\":\"f\"}}\n\n",
		"event: response.reasoning_summary_text.delta\ndata: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"think\"}\n\n",
	}
	for i, evt := range visible {
		if !StreamEventsHaveVisibleOutput("openai-response", [][]byte{[]byte(evt)}) {
			t.Fatalf("responses event %d carries visible output but guard missed it", i)
		}
	}
	bookkeeping := []string{
		"event: response.created\ndata: {\"type\":\"response.created\"}\n\n",
		"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"message\"}}\n\n",
		"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"message\"}}\n\n",
		"data: [DONE]\n\n",
	}
	for i, evt := range bookkeeping {
		if StreamEventsHaveVisibleOutput("openai-response", [][]byte{[]byte(evt)}) {
			t.Fatalf("responses bookkeeping %d wrongly counted visible: %s", i, evt)
		}
	}
}
