package shared

import (
	"bytes"
	"testing"
)

func TestJoinStreamEventsWrapsBareOpenAIEvents(t *testing.T) {
	got := JoinStreamEvents("openai", [][]byte{
		[]byte(`{"id":"1","choices":[{"delta":{"content":"he"}}]}`),
		[]byte(`{"id":"2","choices":[{"delta":{"content":"llo"},"finish_reason":"stop"}]}`),
	})
	want := "data: {\"id\":\"1\",\"choices\":[{\"delta\":{\"content\":\"he\"}}]}\n\n" +
		"data: {\"id\":\"2\",\"choices\":[{\"delta\":{\"content\":\"llo\"},\"finish_reason\":\"stop\"}]}\n\n"
	if string(got) != want {
		t.Fatalf("joined openai payload = %q, want %q", got, want)
	}
}

func TestJoinStreamEventsKeepsFramedOpenAIEventsVerbatim(t *testing.T) {
	framed := "data: {\"id\":\"1\"}\n\n"
	got := JoinStreamEvents("openai", [][]byte{[]byte(framed)})
	if !bytes.Equal(got, []byte(framed)) {
		t.Fatalf("framed openai event mutated: %q", got)
	}
}

func TestJoinStreamEventsPassesClaudeResponseFramesByteIdentical(t *testing.T) {
	// Passthrough raw blocks (openai-response) and synthesized Claude events
	// are complete SSE frames; joining must not alter their bytes.
	frame := "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	got := JoinStreamEvents("openai-response", [][]byte{[]byte(frame), []byte(frame)})
	if string(got) != frame+frame {
		t.Fatalf("response frames altered:\n%q", got)
	}
}

func TestJoinStreamEventsTerminatesUnterminatedBlock(t *testing.T) {
	// Defensive: a converter block without its blank-line terminator must
	// gain one so batched blocks cannot fuse into one SSE event.
	got := JoinStreamEvents("claude", [][]byte{[]byte("event: message_start\ndata: {\"type\":\"message_start\"}")})
	want := "event: message_start\ndata: {\"type\":\"message_start\"}\n\n"
	if string(got) != want {
		t.Fatalf("unterminated block = %q, want %q", got, want)
	}
}

func TestJoinStreamEventsEmpty(t *testing.T) {
	if got := JoinStreamEvents("openai", nil); got != nil {
		t.Fatalf("nil events produced %q", got)
	}
	if got := JoinStreamEvents("claude", [][]byte{{}, []byte(" \n")}); got != nil {
		t.Fatalf("blank events produced %q", got)
	}
}
