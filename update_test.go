package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateCheckAndVerifiedDownload(t *testing.T) {
	payload := []byte("MZ Offer Atlas verified update")
	digest := sha256.Sum256(payload)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/latest":
			_, _ = fmt.Fprintf(writer, `{"tag_name":"v0.2.1","body":"## 新增\n- 自动更新","html_url":"https://example.test/release","published_at":"2026-08-24T00:00:00Z","assets":[{"name":"OfferAtlas-windows-amd64.exe","browser_download_url":%q,"size":%d,"digest":"sha256:%s"}]}`,
				server.URL+"/asset", len(payload), hex.EncodeToString(digest[:]))
		case "/asset":
			_, _ = writer.Write(payload)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	manager := testUpdateManager(t, server.URL+"/latest")
	status, err := manager.check(context.Background(), true)
	if err != nil {
		t.Fatalf("check update: %v", err)
	}
	if !status.Available || status.LatestVersion != "0.2.1" || status.State != "available" {
		t.Fatalf("unexpected available status: %#v", status)
	}
	status, err = manager.download(context.Background())
	if err != nil {
		t.Fatalf("download update: %v", err)
	}
	if status.State != "downloaded" || status.DownloadedBytes != int64(len(payload)) {
		t.Fatalf("unexpected downloaded status: %#v", status)
	}
	if _, err := os.Stat(manager.downloadPath); err != nil {
		t.Fatalf("verified package was not retained: %v", err)
	}
}

func TestUpdateRejectsMissingDigest(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/latest" {
			_, _ = fmt.Fprintf(writer, `{"tag_name":"v0.2.1","assets":[{"name":"OfferAtlas-windows-amd64.exe","browser_download_url":%q,"size":2,"digest":""}]}`, server.URL+"/asset")
			return
		}
		_, _ = writer.Write([]byte("MZ"))
	}))
	defer server.Close()
	manager := testUpdateManager(t, server.URL+"/latest")
	if _, err := manager.check(context.Background(), true); err != nil {
		t.Fatalf("check update: %v", err)
	}
	status, err := manager.download(context.Background())
	if err == nil || status.State != "failed" || !strings.Contains(status.Message, "SHA-256") {
		t.Fatalf("missing digest must be rejected, status=%#v err=%v", status, err)
	}
}

func TestUpdateVersionComparisonAndHelperScript(t *testing.T) {
	if !versionGreater("0.10.0", "0.2.9") || versionGreater("0.2.0", "0.2.0") || versionGreater("invalid", "0.1.0") {
		t.Fatal("unexpected semantic version comparison")
	}
	script := updateHelperScript(1234, `D:\Offer Atlas\OfferAtlas.exe`, `C:\Users\test\AppData\OfferAtlas\updates\OfferAtlas-0-2-1.exe`)
	for _, required := range []string{"$oldPid = 1234", "while (Get-Process", "Copy-Item", "Move-Item", "Start-Process"} {
		if !strings.Contains(script, required) {
			t.Fatalf("update helper is missing %q", required)
		}
	}
	if strings.Contains(script, "offer-atlas.db") || strings.Contains(script, "attachments") {
		t.Fatal("update helper must not touch application data")
	}
}

func testUpdateManager(t *testing.T, apiURL string) *updateManager {
	t.Helper()
	directory := t.TempDir()
	return &updateManager{
		status:         AppUpdate{CurrentVersion: "0.2.0", State: "idle", Message: "可检查应用更新"},
		apiURL:         apiURL,
		client:         &http.Client{},
		stagingDir:     directory,
		statePath:      filepath.Join(directory, "state.json"),
		executablePath: filepath.Join(directory, "OfferAtlas.exe"),
		launch:         func(string, ...string) error { return nil },
	}
}
