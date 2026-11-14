// TEMP DIAGNOSTIC: covers the tool-args guard dump switch and sink; remove
// together with internal/adapter/shared/toolargs_dump.go.
package shared

import (
	"os"
	"path/filepath"
	"testing"
)

func TestToolArgsDumpGatingAndAppend(t *testing.T) {
	log := filepath.Join(t.TempDir(), "toolargs.log")
	defer SetToolArgsDump(false, "")

	SetToolArgsDump(false, log)
	AppendToolArgsDump("disabled\n")
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatalf("append while disabled touched the file: %v", err)
	}

	if ToolArgsDumpEnabled() {
		t.Fatalf("ToolArgsDumpEnabled = true while disabled")
	}
	SetToolArgsDump(true, log)
	if !ToolArgsDumpEnabled() {
		t.Fatalf("ToolArgsDumpEnabled = false while enabled")
	}
	AppendToolArgsDump("line-one\n")
	AppendToolArgsDump("line-two\n")
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("read dump: %v", err)
	}
	if string(b) != "line-one\nline-two\n" {
		t.Fatalf("dump content = %q, want %q", b, "line-one\nline-two\n")
	}

	SetToolArgsDump(false, log)
	AppendToolArgsDump("nope\n")
	b, err = os.ReadFile(log)
	if err != nil {
		t.Fatalf("read dump: %v", err)
	}
	if string(b) != "line-one\nline-two\n" {
		t.Fatalf("append after disable changed content: %q", b)
	}
}
