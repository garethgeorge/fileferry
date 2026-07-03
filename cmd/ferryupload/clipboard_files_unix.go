//go:build !darwin && !windows

package main

import "golang.design/x/clipboard"

// clipboardFiles returns the local paths of items copied in a file manager on
// X11/Wayland desktops. GNOME/Nautilus (and other GLib apps) use
// "x-special/gnome-copied-files" — an action word ("copy"/"cut") followed by
// file:// URIs — while other desktops use the generic "text/uri-list". Neither
// is a built-in FmtText/FmtImage, so the clipboard library exposes them only via
// a registered custom format read by name.
func clipboardFiles() []string {
	if raw := clipboard.Read(clipboard.Register("x-special/gnome-copied-files")); len(raw) > 0 {
		if paths := parseURIList(string(raw), true); len(paths) > 0 {
			return paths
		}
	}
	if raw := clipboard.Read(clipboard.Register("text/uri-list")); len(raw) > 0 {
		if paths := parseURIList(string(raw), false); len(paths) > 0 {
			return paths
		}
	}
	return nil
}
