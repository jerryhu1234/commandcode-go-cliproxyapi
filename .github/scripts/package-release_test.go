package main

import (
	"archive/zip"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

func TestPackageLibraryKeepsRuntimeNameAndExecutableMode(t *testing.T) {
	dir := t.TempDir()
	library := filepath.Join(dir, "commandcode-go-cliproxyapi.so")
	archive := filepath.Join(dir, "commandcode-go-cliproxyapi_0.2.0_linux_amd64.zip")
	if err := os.WriteFile(library, []byte("plugin-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := packageLibrary(library, archive); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.OpenReader(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	if len(zr.File) != 1 || zr.File[0].Name != "commandcode-go-cliproxyapi.so" {
		t.Fatalf("archive entries = %+v", zr.File)
	}
	if zr.File[0].Mode().Perm() != 0o755 {
		t.Fatalf("runtime mode = %o, want 755", zr.File[0].Mode().Perm())
	}
}

func TestPackageCommandWritesProvenance(t *testing.T) {
	dir := t.TempDir()
	library := filepath.Join(dir, "commandcode-go-cliproxyapi.so")
	archive := filepath.Join(dir, "commandcode-go-cliproxyapi_0.2.0_linux_amd64.zip")
	checksum := archive + ".sha256"
	if err := os.WriteFile(library, []byte("plugin-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VERSION", "0.2.0")
	t.Setenv("COMMIT", "deadbeef")
	oldArgs, oldFlags := os.Args, flag.CommandLine
	os.Args = []string{"package-release", "-library", library, "-archive", archive, "-checksum", checksum}
	flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
	t.Cleanup(func() { os.Args, flag.CommandLine = oldArgs, oldFlags })
	main()
	raw, err := os.ReadFile(archive + ".provenance.json")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Version, Commit, Archive, SHA256 string
	}
	if json.Unmarshal(raw, &got) != nil || got.Version != "0.2.0" || got.Commit != "deadbeef" || got.Archive != filepath.Base(archive) || got.SHA256 == "" {
		t.Fatalf("provenance = %+v (%s)", got, raw)
	}
}
