// Package buildinfo contains metadata injected into release binaries.
package buildinfo

// Version is the plugin version reported to CLIProxyAPI. Release builds set
// this to the tag without its leading "v" using -ldflags -X. It matches the
// artifact filename's v0.2.9 segment and CPA 8.6 compatibility.
var Version = "0.2.9"

// Commit identifies the source revision without changing Version's semver
// shape. Release and CI builds set it independently using -ldflags -X.
var Commit = "unknown"
