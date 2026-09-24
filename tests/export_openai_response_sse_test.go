package tests

import (
	"commandcode-go-cliproxyapi/internal/adapter/chatcompletions"
	"os"
	"testing"
)

// Run manually with COMMANDCODE_SSE_OUT=/owned/path go test ./tests -run TestExportOpenAIResponseSSE.
// It exports real converter output for the external openai-node harness.
func TestExportOpenAIResponseSSE(t *testing.T) {
	path := os.Getenv("COMMANDCODE_SSE_OUT")
	if path == "" {
		t.Skip("COMMANDCODE_SSE_OUT unset")
	}
	sc := chatcompletions.NewStreamConverter("openai-response")
	chunks := []string{`data: {"id":"sdk","model":"m","choices":[{"delta":{"content":"before"}}]}` + "\n", `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c","function":{"name":"f","arguments":"{}"}}]}}]}` + "\n", `data: {"choices":[{"delta":{"content":"after"},"finish_reason":"stop"}]}` + "\n", `data: {"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":2}}` + "\n", `data: [DONE]` + "\n"}
	var out []byte
	for _, chunk := range chunks {
		events, _, e := sc.Feed([]byte(chunk))
		if e != nil {
			t.Fatal(e)
		}
		for _, event := range events {
			out = append(out, event...)
		}
	}
	if e := os.WriteFile(path, out, 0600); e != nil {
		t.Fatal(e)
	}
}
