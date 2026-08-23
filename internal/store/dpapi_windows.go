//go:build windows

package store

import (
	"fmt"
	"syscall"
	"unsafe"
)

type dataBlob struct {
	cbData uint32
	pbData *byte
}

var (
	crypt32                = syscall.NewLazyDLL("crypt32.dll")
	kernel32               = syscall.NewLazyDLL("kernel32.dll")
	cryptProtectDataProc   = crypt32.NewProc("CryptProtectData")
	cryptUnprotectDataProc = crypt32.NewProc("CryptUnprotectData")
	localFreeProc          = kernel32.NewProc("LocalFree")
)

func protectLocalSecret(value []byte) ([]byte, error) {
	if len(value) == 0 {
		return nil, fmt.Errorf("secret is empty")
	}
	in := dataBlob{cbData: uint32(len(value)), pbData: &value[0]}
	var out dataBlob
	ok, _, err := cryptProtectDataProc.Call(
		uintptr(unsafe.Pointer(&in)), 0, 0, 0, 0, 0x1, uintptr(unsafe.Pointer(&out)), // CRYPTPROTECT_UI_FORBIDDEN
	)
	if ok == 0 {
		if err != syscall.Errno(0) {
			return nil, fmt.Errorf("protect token with Windows DPAPI: %w", err)
		}
		return nil, fmt.Errorf("protect token with Windows DPAPI")
	}
	defer localFreeProc.Call(uintptr(unsafe.Pointer(out.pbData)))
	return append([]byte(nil), unsafe.Slice(out.pbData, out.cbData)...), nil
}

func unprotectLocalSecret(value []byte) ([]byte, error) {
	if len(value) == 0 {
		return nil, fmt.Errorf("protected secret is empty")
	}
	in := dataBlob{cbData: uint32(len(value)), pbData: &value[0]}
	var out dataBlob
	ok, _, err := cryptUnprotectDataProc.Call(
		uintptr(unsafe.Pointer(&in)), 0, 0, 0, 0, 0x1, uintptr(unsafe.Pointer(&out)),
	)
	if ok == 0 {
		if err != syscall.Errno(0) {
			return nil, fmt.Errorf("read token with Windows DPAPI: %w", err)
		}
		return nil, fmt.Errorf("read token with Windows DPAPI")
	}
	defer localFreeProc.Call(uintptr(unsafe.Pointer(out.pbData)))
	return append([]byte(nil), unsafe.Slice(out.pbData, out.cbData)...), nil
}
