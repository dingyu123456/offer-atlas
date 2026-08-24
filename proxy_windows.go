//go:build windows

package main

import (
	"fmt"
	"net"
	"net/url"
	"strings"

	"golang.org/x/sys/windows/registry"
)

const internetSettingsRegistryPath = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`

// systemProxyForURL reads the current user's static Windows proxy settings.
// PAC/WPAD is intentionally left to the browser/TUN layer; static proxy
// settings cover the common rule-mode clients without guessing a port.
func systemProxyForURL(target *url.URL) (*url.URL, bool) {
	key, err := registry.OpenKey(registry.CURRENT_USER, internetSettingsRegistryPath, registry.QUERY_VALUE)
	if err != nil {
		return nil, false
	}
	defer key.Close()
	enabled, _, err := key.GetIntegerValue("ProxyEnable")
	if err != nil || enabled == 0 {
		return nil, false
	}
	server, _, err := key.GetStringValue("ProxyServer")
	if err != nil || strings.TrimSpace(server) == "" {
		return nil, false
	}
	override, _, _ := key.GetStringValue("ProxyOverride")
	if proxyHostBypassed(target.Hostname(), override) {
		return nil, false
	}
	proxy, err := parseWindowsProxyServer(server, target.Scheme)
	if err != nil {
		return nil, false
	}
	return proxy, true
}

func parseWindowsProxyServer(value, targetScheme string) (*url.URL, error) {
	parts := strings.Split(value, ";")
	byScheme := make(map[string]string, len(parts))
	var fallback string
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if key, item, ok := strings.Cut(part, "="); ok {
			byScheme[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(item)
			continue
		}
		if fallback == "" {
			fallback = part
		}
	}
	server := byScheme[strings.ToLower(targetScheme)]
	if server == "" {
		server = byScheme["http"]
	}
	if server == "" {
		server = fallback
	}
	if server == "" {
		return nil, fmt.Errorf("Windows system proxy is empty")
	}
	if !strings.Contains(server, "://") {
		server = "http://" + server
	}
	proxy, err := url.Parse(server)
	if err != nil || proxy.Host == "" {
		return nil, fmt.Errorf("invalid Windows system proxy %q", server)
	}
	if _, _, err := net.SplitHostPort(proxy.Host); err != nil && !strings.Contains(proxy.Host, ":") {
		return nil, fmt.Errorf("Windows system proxy has no port")
	}
	return proxy, nil
}

func proxyHostBypassed(host, override string) bool {
	for _, item := range strings.Split(override, ";") {
		item = strings.TrimSpace(strings.ToLower(item))
		if item == "" {
			continue
		}
		if item == "<local>" && !strings.Contains(host, ".") {
			return true
		}
		if strings.HasPrefix(item, "*.") && strings.HasSuffix(host, item[1:]) {
			return true
		}
		if strings.HasPrefix(item, "*") && strings.HasSuffix(host, strings.TrimPrefix(item, "*")) {
			return true
		}
		if strings.HasSuffix(item, "*") && strings.HasPrefix(host, strings.TrimSuffix(item, "*")) {
			return true
		}
		if item == host {
			return true
		}
	}
	return false
}
