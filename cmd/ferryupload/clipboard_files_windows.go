//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

// Windows exposes files copied in Explorer as the CF_HDROP clipboard format,
// which golang.design/x/clipboard does not surface, so clipboardFiles reads it
// directly through the Win32 API (user32 + shell32).
var (
	user32  = syscall.NewLazyDLL("user32.dll")
	shell32 = syscall.NewLazyDLL("shell32.dll")

	procIsClipboardFormatAvailable = user32.NewProc("IsClipboardFormatAvailable")
	procOpenClipboard              = user32.NewProc("OpenClipboard")
	procCloseClipboard             = user32.NewProc("CloseClipboard")
	procGetClipboardData           = user32.NewProc("GetClipboardData")
	procDragQueryFileW             = shell32.NewProc("DragQueryFileW")
)

const cfHDROP = 15

// clipboardFiles returns the paths of files/folders copied in Explorer, or nil
// when the clipboard holds no CF_HDROP data.
func clipboardFiles() []string {
	if r, _, _ := procIsClipboardFormatAvailable.Call(cfHDROP); r == 0 {
		return nil
	}
	if r, _, _ := procOpenClipboard.Call(0); r == 0 {
		return nil
	}
	defer procCloseClipboard.Call()

	// GetClipboardData returns a handle owned by the clipboard; it must not be
	// freed. DragQueryFileW accepts it as an HDROP and locks it internally.
	hDrop, _, _ := procGetClipboardData.Call(cfHDROP)
	if hDrop == 0 {
		return nil
	}

	// Index 0xFFFFFFFF asks DragQueryFileW for the file count.
	count, _, _ := procDragQueryFileW.Call(hDrop, 0xFFFFFFFF, 0, 0)
	paths := make([]string, 0, count)
	for i := range count {
		// A nil buffer returns the length in characters, excluding the NUL.
		n, _, _ := procDragQueryFileW.Call(hDrop, i, 0, 0)
		if n == 0 {
			continue
		}
		buf := make([]uint16, n+1)
		procDragQueryFileW.Call(hDrop, i, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		paths = append(paths, syscall.UTF16ToString(buf))
	}
	return paths
}
