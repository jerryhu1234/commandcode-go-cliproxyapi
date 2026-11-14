//go:build debug

package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"commandcode-go-cliproxyapi/internal/errclass"
)

// TestDebugTraceRedactsSecrets pins the debug sink's data-safety contract:
// debugTrace is a developer instrumentation, but it cannot rely on every
// caller pre-redacting raw error values (network failures, upstream error
// echoes), so the sink itself redacts bearer tokens, x-api-key/api-key
// header echoes, and query-string keys before writing the line.
func TestDebugTraceRedactsSecrets(t *testing.T) {
	dir := t.TempDir()
	// debugTrace writes next to os.Args[0]; point it at a clean temp dir.
	saved := os.Args[0]
	os.Args[0] = filepath.Join(dir, "testbin")
	t.Cleanup(func() { os.Args[0] = saved })

	const secret = "sk-secret-abc123456789"
	debugTrace("executor stream read chunk_len=%d closed=%t readErrMsg=%q err=%v",
		42, true, "invalid x-api-key: "+secret, "Bearer "+secret)

	out, err := os.ReadFile(filepath.Join(dir, "commandcode-debug.log"))
	if err != nil {
		t.Fatalf("debug log unreadable: %v", err)
	}
	line := string(out)
	if !strings.Contains(line, "executor stream read") {
		t.Fatalf("debug line missing: %q", line)
	}
	for _, leak := range []string{secret, "[redacted]x", "x-[redacted]"} {
		if strings.Contains(line, leak) {
			t.Errorf("debug line leaked %q: %q", leak, line)
		}
	}
	for _, want := range []string{"[redacted]", "x-api-key: [redacted]", "Bearer [redacted]"} {
		if !strings.Contains(line, want) {
			t.Errorf("debug line missing %q: %q", want, line)
		}
	}
	if !strings.Contains(line, "42") {
		t.Errorf("debug line lost non-secret metadata: %q", line)
	}
	// The same string redacts idempotently through the shared kernel.
	if errclass.Redact("x-api-key: [redacted]") != "x-api-key: [redacted]" {
		t.Error("shared Redact lost idempotency")
	}
}
