//go:build windows

package main

import "testing"

func TestParseWindowsProxyServer(t *testing.T) {
	proxy, err := parseWindowsProxyServer("http=127.0.0.1:7890;https=127.0.0.1:7897", "https")
	if err != nil || proxy.String() != "http://127.0.0.1:7897" {
		t.Fatalf("unexpected HTTPS proxy: %v, %v", proxy, err)
	}
	proxy, err = parseWindowsProxyServer("127.0.0.1:7890", "https")
	if err != nil || proxy.String() != "http://127.0.0.1:7890" {
		t.Fatalf("unexpected shared proxy: %v, %v", proxy, err)
	}
}

func TestProxyHostBypassed(t *testing.T) {
	if !proxyHostBypassed("localhost", "<local>;*.internal") {
		t.Fatal("expected local host to be bypassed")
	}
	if !proxyHostBypassed("api.internal", "<local>;*.internal") {
		t.Fatal("expected wildcard host to be bypassed")
	}
	if proxyHostBypassed("api.github.com", "<local>;*.internal") {
		t.Fatal("unexpected GitHub bypass")
	}
}
