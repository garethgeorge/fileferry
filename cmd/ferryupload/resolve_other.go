//go:build !darwin

package main

// resolveFileURL is macOS-specific (see resolve_darwin.go). Elsewhere,
// freedesktop file:// URIs already carry real, percent-decoded paths, and
// Windows uses CF_HDROP paths directly, so no resolution is needed.
func resolveFileURL(string) (string, bool) { return "", false }
