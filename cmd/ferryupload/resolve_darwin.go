//go:build darwin

package main

import (
	"unsafe"

	"github.com/ebitengine/purego/objc"
)

// A macOS "Copy" in Finder places a file-reference URL on the pasteboard —
// file:///.file/id=6571367.61208164 rather than a plain path. POSIX stat cannot
// follow it; only Cocoa can turn it back into a concrete path. These bindings
// call NSURL to do that (see resolveFileURL). The clipboard library we depend on
// already links purego/objc, so this adds no new dependency.
const nsUTF8StringEncoding = 4

var (
	classNSURL             = objc.GetClass("NSURL")
	classNSString          = objc.GetClass("NSString")
	classNSAutoreleasePool = objc.GetClass("NSAutoreleasePool")

	selURLWithString        = objc.RegisterName("URLWithString:")
	selStringWithUTF8String = objc.RegisterName("stringWithUTF8String:")
	selPath                 = objc.RegisterName("path")
	selLengthOfBytes        = objc.RegisterName("lengthOfBytesUsingEncoding:")
	selGetCString           = objc.RegisterName("getCString:maxLength:encoding:")
	selAlloc                = objc.RegisterName("alloc")
	selInit                 = objc.RegisterName("init")
	selDrain                = objc.RegisterName("drain")
)

// resolveFileURL converts a file:// URL — including a Finder file-reference URL
// (file:///.file/id=…) — to a concrete filesystem path via NSURL's -path, which
// resolves the reference. It returns ok=false when the string is not a usable
// file URL (e.g. the referenced item no longer exists). For a plain path URL
// such as file:///etc/hosts, -path simply returns "/etc/hosts", so this is safe
// to call for every file URL on macOS.
func resolveFileURL(uri string) (string, bool) {
	pool := objc.ID(classNSAutoreleasePool).Send(selAlloc).Send(selInit)
	defer pool.Send(selDrain)

	// purego marshals the Go string to a NUL-terminated C string for the call.
	ns := objc.ID(classNSString).Send(selStringWithUTF8String, uri)
	if ns == 0 {
		return "", false
	}
	u := objc.ID(classNSURL).Send(selURLWithString, ns)
	if u == 0 {
		return "", false
	}
	p := u.Send(selPath)
	if p == 0 {
		return "", false
	}
	path := goStringFromNS(p)
	return path, path != ""
}

// goStringFromNS copies an NSString's UTF-8 bytes into a Go string via
// -getCString:maxLength:encoding:, which writes into a Go-owned buffer (so there
// is no unsafe uintptr→pointer conversion of foreign memory).
func goStringFromNS(s objc.ID) string {
	n := int(s.Send(selLengthOfBytes, nsUTF8StringEncoding))
	if n <= 0 {
		return ""
	}
	buf := make([]byte, n+1)
	if ok := s.Send(selGetCString, unsafe.Pointer(&buf[0]), n+1, nsUTF8StringEncoding); ok == 0 {
		return ""
	}
	return string(buf[:n])
}
