//go:build !windows

package store

import "fmt"

// Synchronization V1 is Windows-only. Keeping an explicit stub lets package
// tests compile on other hosts without ever silently storing a token there.
func protectLocalSecret([]byte) ([]byte, error) {
	return nil, fmt.Errorf("Gitee token protection is available on Windows only")
}

func unprotectLocalSecret([]byte) ([]byte, error) {
	return nil, fmt.Errorf("Gitee token protection is available on Windows only")
}
