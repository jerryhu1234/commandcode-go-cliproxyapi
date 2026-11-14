// TEMP DIAGNOSTIC: process-local switch and append-only dump sink for the
// terminal tool-args guard ("tool call arguments were incomplete at stream
// completion"). The switch is set from the plugin config
// (debug-tool-args-dump) during register/reconfigure so operators can turn
// the dump on and off without rebuilding the plugin shared library. Remove
// the whole file together with the adapter dump sites once the guard's
// attribution is settled.
package shared

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
)

// DefaultToolArgsDumpFile is the log appended to when the dump is enabled;
// it resolves next to the host binary unless an explicit path is set.
const DefaultToolArgsDumpFile = "commandcode-toolargs.log"

var (
	toolArgsDumpOn     atomic.Bool
	toolArgsDumpPathMu sync.Mutex
	toolArgsDumpPath   string
)

// SetToolArgsDump flips the terminal tool-args guard dump. An empty path
// re-resolves next to the host binary directory on the first append.
func SetToolArgsDump(on bool, path string) {
	toolArgsDumpPathMu.Lock()
	defer toolArgsDumpPathMu.Unlock()
	toolArgsDumpOn.Store(on)
	toolArgsDumpPath = path
}

// ToolArgsDumpEnabled reports whether the dump sink is active; adapters
// short-circuit their dump walks on it.
func ToolArgsDumpEnabled() bool {
	return toolArgsDumpOn.Load()
}

// AppendToolArgsDump writes one pre-formatted diagnostic line to the dump
// file, no-op when the dump is disabled or the file cannot be opened.
func AppendToolArgsDump(line string) {
	toolArgsDumpPathMu.Lock()
	defer toolArgsDumpPathMu.Unlock()
	if !toolArgsDumpOn.Load() {
		return
	}
	path := toolArgsDumpPath
	if path == "" {
		path = filepath.Join(filepath.Dir(os.Args[0]), DefaultToolArgsDumpFile)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(line)
}
