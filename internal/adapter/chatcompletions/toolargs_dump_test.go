// TEMP DIAGNOSTIC: verifies the tool-args guard dump is config-gated,
// captures the accumulated-args evidence, and stays silent on valid
// tool calls. Remove together with dumpBadTools in stream.go.
package chatcompletions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"commandcode-go-cliproxyapi/internal/adapter/shared"
	"commandcode-go-cliproxyapi/internal/errclass"
)

// toolFrame renders one upstream Chat Completions data: line carrying an
// optional tool_calls delta for index 0 (id/name/arguments), an optional
// finish_reason, and an optional usage trailer. json.Marshal owns every
// escape; no hand-written JSON here.
func toolFrame(t *testing.T, id, name, args, finish string, usage bool) string {
	t.Helper()
	delta := map[string]any{}
	if name != "" || (id != "" && args != "") || args != "" {
		tc := map[string]any{"index": 0}
		if id != "" {
			tc["id"] = id
		}
		if name != "" {
			tc["type"] = "function"
		}
		tc["function"] = map[string]any{"name": name, "arguments": args}
		delta["tool_calls"] = []any{tc}
	}
	if finish != "" {
		delta["finish_reason"] = finish
	}
	choice := map[string]any{"index": 0, "delta": delta}
	if usage {
		choice["usage"] = map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}
	}
	raw, err := json.Marshal(map[string]any{"id": "c1", "model": "m", "choices": []any{choice}})
	if err != nil {
		t.Fatalf("marshal chunk: %v", err)
	}
	return "data: " + string(raw) + "\n\n"
}

func doneFrame() string { return "data: [DONE]\n\n" }

// brokenToolFrames: fragment one opens the JSON object ({"path), fragment
// two arrives bound to a DIFFERENT call id on the same index and its text
// never closes the object — the concatenation stays invalid while the
// upstream still terminates cleanly with finish_reason and [DONE].
func brokenToolFrames(t *testing.T) []string {
	t.Helper()
	return []string{
		toolFrame(t, "call_1", "fs_write", "{\"path", "", false),
		toolFrame(t, "call_2", "", ":/tmp/x}", "", false),
		toolFrame(t, "", "", "", "tool_calls", false),
		toolFrame(t, "", "", "", "tool_calls", true),
		doneFrame(),
	}
}

// validToolFrames: same two-fragment shape, but the fragments complete a
// well-formed JSON object and the guard must stay silent.
func validToolFrames(t *testing.T) []string {
	t.Helper()
	return []string{
		toolFrame(t, "call_1", "fs_write", "{\"path", "", false),
		toolFrame(t, "", "", "\":\"/tmp/x\"}", "", false),
		toolFrame(t, "", "", "", "tool_calls", false),
		doneFrame(),
	}
}

func feedFrames(t *testing.T, frames []string) (done bool, eErr *errclass.Error) {
	t.Helper()
	sc := NewStreamConverter("openai")
	for i, frame := range frames {
		events, d, e := sc.Feed([]byte(frame))
		_ = events
		if e != nil {
			return d, e
		}
		if d {
			if i != len(frames)-1 {
				t.Fatalf("stream done early at frame %d/%d", i, len(frames))
			}
			return true, nil
		}
	}
	return false, nil
}

func TestToolArgsGuardDumpGated(t *testing.T) {
	log := filepath.Join(t.TempDir(), "toolargs.log")
	defer shared.SetToolArgsDump(false, "")

	// Default (off): the guard still fails the stream, but nothing is written.
	shared.SetToolArgsDump(false, log)
	done, eErr := feedFrames(t, brokenToolFrames(t))
	if eErr == nil || !strings.Contains(eErr.Message, "tool call arguments were incomplete") {
		t.Fatalf("guard error = %v, want incomplete-args translation error", eErr)
	}
	if done {
		t.Fatalf("done = true alongside guard failure")
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatalf("disabled dump wrote to %s: %v", log, err)
	}
}

func TestToolArgsGuardDumpCapturesEvidence(t *testing.T) {
	log := filepath.Join(t.TempDir(), "toolargs.log")
	defer shared.SetToolArgsDump(false, "")

	shared.SetToolArgsDump(true, log)
	done, eErr := feedFrames(t, brokenToolFrames(t))
	if eErr == nil || !strings.Contains(eErr.Message, "tool call arguments were incomplete") {
		t.Fatalf("guard error = %v, want incomplete-args translation error", eErr)
	}
	if done {
		t.Fatalf("done = true alongside guard failure")
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("read dump: %v", err)
	}
	lines := strings.TrimSpace(string(b))
	if got := len(strings.Split(lines, "\n")); got != 1 {
		t.Fatalf("dump lines = %d, want 1:\n%s", got, lines)
	}
	for _, want := range []string{
		"CCDBG tool site=cc_done",
		`finish=""`,
		"src=openai",
		"idx=0",
		"bad=true",
		"frags=2",
		"id_changed=1", // second fragment's call id differs: index omission/reuse fingerprint
		`last_id="call_2"`,
		`id="call_1"`,
		`name="fs_write"`,
		"args_len=14",
	} {
		if !strings.Contains(lines, want) {
			t.Fatalf("dump missing %q:\n%s", want, lines)
		}
	}
	// %q escapes the accumulated quote: {\"path:/tmp/x} — both fragments
	// concatenated into one argument string is the truncation evidence.
	if !strings.Contains(lines, `{\"path:/tmp/x}`) {
		t.Fatalf("dump sample lacks concatenated args evidence:\n%s", lines)
	}
}

func TestToolArgsGuardDumpSilentOnValidStream(t *testing.T) {
	log := filepath.Join(t.TempDir(), "toolargs.log")
	defer shared.SetToolArgsDump(false, "")

	shared.SetToolArgsDump(true, log)
	done, eErr := feedFrames(t, validToolFrames(t))
	if eErr != nil {
		t.Fatalf("valid stream failed: %v", eErr)
	}
	if !done {
		t.Fatalf("valid stream not done")
	}
	// The dump file must not even be created by a healthy stream.
	found, err := os.ReadDir(filepath.Dir(log))
	if err != nil || len(found) != 0 {
		t.Fatalf("valid stream left dump files: %v", err)
	}
}
