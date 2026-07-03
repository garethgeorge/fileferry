//go:build darwin

package main

import "golang.design/x/clipboard"

// clipboardFiles returns the local paths of items copied in Finder. macOS
// advertises a copied item as the "public.file-url" pasteboard type, which the
// clipboard library filters out of Formats() (it isn't MIME-shaped), so we read
// it by name. The general pasteboard exposes a single file URL — usually a
// file-reference URL that fileURIToPath resolves to a real path — so only the
// first of several copied items is returned.
func clipboardFiles() []string {
	raw := clipboard.Read(clipboard.Register("public.file-url"))
	if len(raw) == 0 {
		return nil
	}
	if p, ok := fileURIToPath(string(raw)); ok {
		return []string{p}
	}
	return nil
}
