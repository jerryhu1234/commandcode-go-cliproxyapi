package buildinfo

import "testing"

func TestDevelopmentMetadata(t *testing.T) {
	if Version != "0.2.0-dev" {
		t.Fatalf("Version = %q, want 0.2.0-dev", Version)
	}
	if Commit == "" {
		t.Fatal("Commit must have a useful fallback")
	}
}
