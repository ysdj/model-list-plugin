//go:build windows

package main

import (
	"errors"
	"syscall"
	"unsafe"
)

// Electron's safeStorage on Windows hands the bytes to DPAPI, after a "v10"
// version marker.  The blob is therefore readable by any process running as the
// user who wrote it -- which is exactly the hook's position.

type dataBlob struct {
	cbData uint32
	pbData *byte
}

var (
	crypt32DLL    = syscall.NewLazyDLL("crypt32.dll")
	kernel32DLL   = syscall.NewLazyDLL("kernel32.dll")
	unprotectData = crypt32DLL.NewProc("CryptUnprotectData")
	localFree     = kernel32DLL.NewProc("LocalFree")
)

func unsealBytes(raw []byte) (string, error) {
	body := raw
	if len(body) > 3 && string(body[:3]) == "v10" {
		body = body[3:]
	}
	if len(body) == 0 {
		return "", errors.New("sealed value is empty")
	}
	in := dataBlob{cbData: uint32(len(body)), pbData: &body[0]}
	var out dataBlob
	ok, _, callErr := unprotectData.Call(
		uintptr(unsafe.Pointer(&in)),
		0, 0, 0, 0,
		0x1, // CRYPTPROTECT_UI_FORBIDDEN
		uintptr(unsafe.Pointer(&out)),
	)
	if ok == 0 {
		return "", callErr
	}
	defer localFree.Call(uintptr(unsafe.Pointer(out.pbData)))
	if out.pbData == nil || out.cbData == 0 {
		return "", errors.New("sealed value is empty")
	}
	return string(unsafe.Slice(out.pbData, out.cbData)), nil
}
