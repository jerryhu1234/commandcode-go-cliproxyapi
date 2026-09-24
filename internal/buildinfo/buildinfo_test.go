package buildinfo

import "testing"

func TestDevelopmentMetadata(t *testing.T) {
	if Version != "0.2.5" {
		t.Fatalf("Version = %q, want 0.2.5", Version)
	}
	if Commit == "" {
		t.Fatal("Commit must have a useful fallback")
	}
}
