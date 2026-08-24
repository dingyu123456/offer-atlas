//go:build !windows

package main

import "net/url"

func systemProxyForURL(*url.URL) (*url.URL, bool) {
	return nil, false
}
