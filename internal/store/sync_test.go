package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dingyu/offer-atlas/internal/domain"
)

func TestGiteeInitialUploadUsesImmutableOperationsAndKeepsTokenOutOfBackup(t *testing.T) {
	server := newFakeGitee(t)
	defer server.Close()

	store, err := Open(pathJoinTemp(t, "offer-atlas.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	store.cloud.clientFactory = func(token string) *giteeClient {
		return newGiteeClient(token).withBaseURL(server.URL() + "/api/v5")
	}
	company, err := store.SaveCompany(domain.CompanyInput{Name: "Gitee Verification Co."})
	if err != nil {
		t.Fatalf("create source data: %v", err)
	}

	preview, err := store.ConnectGitee(context.Background(), "never-store-this-gitee-token")
	if err != nil {
		t.Fatalf("connect Gitee: %v", err)
	}
	if preview.Recommended != "upload" || preview.PrimaryRepo == "" || preview.Account != "dingyu" {
		t.Fatalf("unexpected connection preview: %#v", preview)
	}
	status, err := store.ConfirmGiteeConnection(context.Background(), "upload")
	if err != nil {
		t.Fatalf("confirm initial upload: %v", err)
	}
	if status.State != "synced" || status.PendingChanges != 0 {
		t.Fatalf("unexpected status after upload: %#v", status)
	}
	if !server.hasFile(preview.PrimaryRepo, syncFormatPath) || server.countPrefix(preview.PrimaryRepo, "operations/") == 0 {
		t.Fatal("initial sync did not create the format marker and immutable operation files")
	}

	backupPath, err := store.CreateBackup()
	if err != nil {
		t.Fatalf("create backup: %v", err)
	}
	backupBytes, err := osReadFile(backupPath)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if strings.Contains(string(backupBytes), "never-store-this-gitee-token") {
		t.Fatal("Gitee token leaked into SQLite backup")
	}
	tokenBytes, err := osReadFile(store.cloud.tokenPath())
	if err != nil {
		t.Fatalf("read protected token: %v", err)
	}
	if strings.Contains(string(tokenBytes), "never-store-this-gitee-token") {
		t.Fatal("Gitee token was not DPAPI protected")
	}
	if err := store.DisconnectGitee(); err != nil {
		t.Fatalf("disconnect Gitee: %v", err)
	}
	if _, err := osStat(store.cloud.tokenPath()); err == nil {
		t.Fatal("disconnect must clear the local DPAPI token file")
	}
	if _, err := store.GetPositionDetail(company.ID); err == nil {
		t.Fatal("sanity guard: a company ID must not resolve as a position")
	}
}

func TestGiteeUploadAfterRemoteRepositoriesWereReplacedRebuildsLocalTracking(t *testing.T) {
	server := newFakeGitee(t)
	defer server.Close()
	store, err := Open(pathJoinTemp(t, "remote-rebuild.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.cloud.clientFactory = func(token string) *giteeClient {
		return newGiteeClient(token).withBaseURL(server.URL() + "/api/v5")
	}
	if _, err := store.SaveCompany(domain.CompanyInput{Name: "Rebuild Co."}); err != nil {
		t.Fatal(err)
	}
	preview, err := store.ConnectGitee(context.Background(), "same-token")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConfirmGiteeConnection(context.Background(), "upload"); err != nil {
		t.Fatal(err)
	}
	if server.countPrefix(preview.PrimaryRepo, "operations/") == 0 {
		t.Fatal("initial upload did not create an operation")
	}
	if err := store.DisconnectGitee(); err != nil {
		t.Fatal(err)
	}
	// Simulate the user removing the dedicated primary/media repositories in
	// Gitee before reconnecting with the same local business data.
	server.mu.Lock()
	for name := range server.repos {
		if isOfferAtlasPrimaryName(name) || isOfferAtlasMediaName(name) {
			delete(server.repos, name)
		}
	}
	server.mu.Unlock()

	rebuilt, err := store.ConnectGitee(context.Background(), "same-token")
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt.Recommended != "upload" {
		t.Fatalf("replaced remote should recommend upload: %#v", rebuilt)
	}
	status, err := store.ConfirmGiteeConnection(context.Background(), "upload")
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "synced" || status.PendingChanges != 0 {
		t.Fatalf("rebuild upload did not finish cleanly: %#v", status)
	}
	if got := server.countPrefix(rebuilt.PrimaryRepo, "operations/"); got != 2 { // one operation plus its discovery index
		t.Fatalf("rebuild should upload a fresh operation set, got %d files", got)
	}
}

func TestGiteeFileWriteCreatesWithPostAndUpdatesWithSHA(t *testing.T) {
	server := newFakeGitee(t)
	defer server.Close()
	server.mu.Lock()
	server.repos["offer-atlas-sync"] = map[string][]byte{}
	server.mu.Unlock()
	client := newGiteeClient("test-token").withBaseURL(server.URL() + "/api/v5")
	if err := client.writeFile(context.Background(), "dingyu", "offer-atlas-sync", "marker.json", "create marker", []byte(`{"version":1}`), ""); err != nil {
		t.Fatalf("create Gitee file with POST: %v", err)
	}
	contents, sha, err := client.readFile(context.Background(), "dingyu", "offer-atlas-sync", "marker.json")
	if err != nil || string(contents) != `{"version":1}` || sha == "" {
		t.Fatalf("read created Gitee file: contents=%q sha=%q err=%v", contents, sha, err)
	}
	if err := client.writeFile(context.Background(), "dingyu", "offer-atlas-sync", "marker.json", "update marker", []byte(`{"version":2}`), sha); err != nil {
		t.Fatalf("update Gitee file with SHA: %v", err)
	}
}

func TestDeleteGiteeSyncRepositoriesOnlyRemovesMarkedPrivateRepositories(t *testing.T) {
	server := newFakeGitee(t)
	defer server.Close()

	store, err := Open(pathJoinTemp(t, "delete-cloud.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.cloud.clientFactory = func(token string) *giteeClient {
		return newGiteeClient(token).withBaseURL(server.URL() + "/api/v5")
	}
	company, err := store.SaveCompany(domain.CompanyInput{Name: "Delete Cloud Co."})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := store.ConnectGitee(context.Background(), "delete-token")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConfirmGiteeConnection(context.Background(), "upload"); err != nil {
		t.Fatal(err)
	}

	// Add marked media/primary repositories and an ordinary same-named repo.
	server.mu.Lock()
	server.repos["offer-atlas-media-001"] = map[string][]byte{mediaFormatPath: []byte(`{"product":"Offer Atlas","format":1,"kind":"media"}`)}
	server.repos["offer-atlas-sync-001"] = map[string][]byte{syncFormatPath: []byte(`{"product":"Offer Atlas","format":1,"kind":"primary"}`)}
	server.repos["offer-atlas-sync-002"] = map[string][]byte{"README.md": []byte("ordinary repository")}
	server.mu.Unlock()

	centerBefore, err := store.BackupCenter()
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := store.DeleteGiteeSyncRepositories(context.Background())
	if err != nil {
		t.Fatalf("delete Offer Atlas repositories: %v", err)
	}
	if len(deleted) != 3 {
		t.Fatalf("expected primary plus two marked repositories to be deleted, got %v", deleted)
	}
	for _, name := range []string{preview.PrimaryRepo, "offer-atlas-media-001", "offer-atlas-sync-001"} {
		if server.hasRepo(name) {
			t.Fatalf("marked repository %q was not deleted", name)
		}
	}
	if !server.hasRepo("offer-atlas-sync-002") {
		t.Fatal("ordinary same-named repository must not be deleted")
	}
	centerAfter, err := store.BackupCenter()
	if err != nil {
		t.Fatal(err)
	}
	if len(centerAfter.Backups) != len(centerBefore.Backups)+1 {
		t.Fatalf("delete should create one local recovery backup: before=%d after=%d", len(centerBefore.Backups), len(centerAfter.Backups))
	}
	status, err := store.CloudSyncStatus()
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "local_only" || status.Owner != "" || status.PrimaryRepo != "" {
		t.Fatalf("delete should disconnect local sync configuration: %#v", status)
	}
	companies, err := store.ListCompanies()
	if err != nil || len(companies) != 1 || companies[0].ID != company.ID {
		t.Fatalf("local business data must remain after deleting cloud repositories: %#v, %v", companies, err)
	}
	if _, err := osStat(store.cloud.tokenPath()); err == nil {
		t.Fatal("delete should clear the local protected token")
	}
}

func TestGiteeClientUsesAuthorizationHeaderWithoutTokenInURL(t *testing.T) {
	var gotAuthorization, gotUserAgent, gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotAuthorization = request.Header.Get("Authorization")
		gotUserAgent = request.Header.Get("User-Agent")
		gotQuery = request.URL.RawQuery
		writeFakeJSON(writer, map[string]any{"login": "dingyu", "name": "DingYu"})
	}))
	defer server.Close()

	client := newGiteeClient("private-token-value").withBaseURL(server.URL)
	if _, err := client.currentUser(context.Background()); err != nil {
		t.Fatalf("current user with header token: %v", err)
	}
	if gotAuthorization != "Bearer private-token-value" {
		t.Fatalf("personal token must use Authorization header, got %q", gotAuthorization)
	}
	if gotQuery != "" || strings.Contains(gotQuery, "private-token-value") {
		t.Fatalf("personal token must not be included in URL query: %q", gotQuery)
	}
	if !strings.HasPrefix(gotUserAgent, "OfferAtlas/") {
		t.Fatalf("expected application user agent, got %q", gotUserAgent)
	}
}

func TestGiteeFileReadTreatsDirectoryResponseAsNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writeFakeJSON(writer, []map[string]any{{"type": "file", "name": "existing.json"}})
	}))
	defer server.Close()

	client := newGiteeClient("private-token-value").withBaseURL(server.URL)
	_, _, err := client.readFile(context.Background(), "dingyu", "offer-atlas-sync", "operations/device/"+operationIndexName)
	var apiErr *giteeError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		t.Fatalf("directory response must be handled as a missing file, got %v", err)
	}
}

func TestGiteeDirectoryListingStopsWhenPaginationIsIgnored(t *testing.T) {
	requests := 0
	page := make([]map[string]any, 100)
	for index := range page {
		page[index] = map[string]any{"type": "file", "name": "same.json", "path": "operations/device/same.json"}
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		writeFakeJSON(writer, page)
	}))
	defer server.Close()

	client := newGiteeClient("private-token-value").withBaseURL(server.URL)
	contents, err := client.listDirectory(context.Background(), "dingyu", "offer-atlas-sync", "operations/device")
	if err != nil {
		t.Fatal(err)
	}
	if len(contents) != 1 || requests != 2 {
		t.Fatalf("ignored pagination must stop after the repeated page: contents=%d requests=%d", len(contents), requests)
	}
}

func TestGiteeWAFResponseIsSanitizedAndRetried(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		writer.WriteHeader(http.StatusForbidden)
		_, _ = writer.Write([]byte(`<!doctype html><div id="baidu_waf_intercept_page">访问存在安全风险</div>`))
	}))
	defer server.Close()

	client := newGiteeClient("private-token-value").withBaseURL(server.URL)
	_, err := client.currentUser(context.Background())
	var apiErr *giteeError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected Gitee API error, got %v", err)
	}
	if !apiErr.TransientWAF || apiErr.actionable() {
		t.Fatalf("WAF 403 must be retried instead of treated as a credential error: %#v", apiErr)
	}
	if strings.Contains(apiErr.Message, "<html") || !strings.Contains(apiErr.Message, "自动重试") {
		t.Fatalf("WAF message must be concise and actionable, got %q", apiErr.Message)
	}
}

func TestGiteeFirstDownloadConfirmationIsSerializedAndIdempotent(t *testing.T) {
	server := newFakeGitee(t)
	defer server.Close()
	configureClient := func(store *Store) {
		store.cloud.clientFactory = func(token string) *giteeClient {
			return newGiteeClient(token).withBaseURL(server.URL() + "/api/v5")
		}
	}

	source, err := Open(pathJoinTemp(t, "confirm-source.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	configureClient(source)
	if _, err := source.SaveCompany(domain.CompanyInput{Name: "Confirmation Co"}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.ConnectGitee(context.Background(), "same-token"); err != nil {
		t.Fatal(err)
	}
	if _, err := source.ConfirmGiteeConnection(context.Background(), "upload"); err != nil {
		t.Fatal(err)
	}

	target, err := Open(pathJoinTemp(t, "confirm-target.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	configureClient(target)
	if preview, err := target.ConnectGitee(context.Background(), "same-token"); err != nil || preview.Recommended != "download" {
		t.Fatalf("prepare first download: %#v, %v", preview, err)
	}

	type confirmationResult struct {
		status domain.CloudSyncStatus
		err    error
	}
	start := make(chan struct{})
	results := make(chan confirmationResult, 2)
	for range 2 {
		go func() {
			<-start
			status, err := target.ConfirmGiteeConnection(context.Background(), "download")
			results <- confirmationResult{status: status, err: err}
		}()
	}
	close(start)
	for range 2 {
		result := <-results
		if result.err != nil || result.status.State != "synced" {
			t.Fatalf("duplicate first-download confirmation must return current status: %#v, %v", result.status, result.err)
		}
	}
	center, err := target.BackupCenter()
	if err != nil || len(center.Backups) != 1 {
		t.Fatalf("duplicate confirmation must create one recovery backup, count=%d err=%v", len(center.Backups), err)
	}
}

func TestGiteePrimaryProvisioningRetryReusesCreatedRepository(t *testing.T) {
	server := newFakeGitee(t)
	defer server.Close()
	server.mu.Lock()
	server.failNextMarkerCreates = 1
	server.mu.Unlock()

	store, err := Open(pathJoinTemp(t, "provisioning-retry.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	store.cloud.clientFactory = func(token string) *giteeClient {
		return newGiteeClient(token).withBaseURL(server.URL() + "/api/v5")
	}

	if _, err := store.ConnectGitee(context.Background(), "same-token"); err == nil {
		t.Fatal("first connection should expose the simulated marker write failure")
	}
	if server.repoCountWithPrefix("offer-atlas-sync") != 1 || !server.hasRepo("offer-atlas-sync") {
		t.Fatalf("first attempt should create exactly one recoverable primary repo, got %#v", server.repoNames())
	}

	preview, err := store.ConnectGitee(context.Background(), "same-token")
	if err != nil {
		t.Fatalf("retry connection: %v", err)
	}
	if preview.PrimaryRepo != "offer-atlas-sync" {
		t.Fatalf("retry must repair the original repo, got %q", preview.PrimaryRepo)
	}
	if server.repoCountWithPrefix("offer-atlas-sync") != 1 || !server.hasFile("offer-atlas-sync", syncFormatPath) {
		t.Fatalf("retry created another repo or did not initialize marker: %#v", server.repoNames())
	}
}

func TestLocalMutationImmediatelyMarksCloudSyncPending(t *testing.T) {
	store, err := Open(pathJoinTemp(t, "local-mutation-pending.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	// Own the timer-driven state below; startup is intentionally asynchronous.
	store.cloud.close()
	if _, err := store.db.Exec(`UPDATE sync_config SET device_id='test-device', owner='dingyu', primary_repo='offer-atlas-sync', enabled=1, initial_sync_pending=0, state='synced' WHERE id=1`); err != nil {
		t.Fatal(err)
	}

	if _, err := store.SaveCompany(domain.CompanyInput{Name: "Pending Immediately Co."}); err != nil {
		t.Fatalf("save company: %v", err)
	}
	status, err := store.CloudSyncStatus()
	if err != nil {
		t.Fatalf("read cloud sync status: %v", err)
	}
	if status.State != "pending" || status.PendingChanges == 0 {
		t.Fatalf("local mutation must immediately become pending, got %#v", status)
	}
	if !strings.Contains(status.Message, "约 10 秒") {
		t.Fatalf("pending state should explain its delayed upload, got %q", status.Message)
	}
}

func TestLocalSyncUsesFirstEditFixedWindow(t *testing.T) {
	store, err := Open(pathJoinTemp(t, "fixed-sync-window.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.cloud.close()
	if _, err := store.db.Exec(`UPDATE sync_config SET device_id='test-device', owner='dingyu', primary_repo='offer-atlas-sync', enabled=1, initial_sync_pending=0, state='synced' WHERE id=1`); err != nil {
		t.Fatal(err)
	}

	store.cloud.scheduleLocalSync(time.Hour)
	store.cloud.scheduleMu.Lock()
	firstTimer := store.cloud.localTimer
	store.cloud.scheduleMu.Unlock()
	if firstTimer == nil {
		t.Fatal("first local edit must schedule a sync window")
	}

	// A second ordinary edit joins the original window rather than moving it.
	store.cloud.scheduleLocalSync(time.Hour)
	store.cloud.scheduleMu.Lock()
	secondTimer := store.cloud.localTimer
	store.cloud.scheduleMu.Unlock()
	if secondTimer != firstTimer {
		t.Fatal("ordinary edits must not reset the first edit sync window")
	}
	store.cloud.cancelLocalTimer()
}

func TestCloudSyncCheckingStateAndCloseDoNotRequireTokenWithoutLocalChanges(t *testing.T) {
	store, err := Open(pathJoinTemp(t, "checking-close.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	// This test owns the state transition below; stop the normal startup worker
	// so it cannot race the deliberately token-less configuration.
	store.cloud.close()
	if _, err := store.db.Exec(`UPDATE sync_config SET device_id='test-device', owner='dingyu', primary_repo='offer-atlas-sync', enabled=1, initial_sync_pending=0, state='syncing' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	// Seeded system node types participate in synchronization. Record a clean
	// baseline so this test represents a real device that already synced them.
	if err := store.cloud.captureLocalChanges(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE sync_operations SET synced_at=?; UPDATE sync_object_states SET dirty=0, remote_version=object_version`, nowString()); err != nil {
		t.Fatal(err)
	}

	status, err := store.CloudSyncStatus()
	if err != nil {
		t.Fatal(err)
	}
	if status.PendingChanges != 0 || status.Message != "正在检查云端是否有更新" {
		t.Fatalf("empty startup work should be presented as a remote check, got %#v", status)
	}
	// No DPAPI token exists in this test. A close with no local operation must
	// only wait for the current check, rather than starting a new remote run.
	if err := store.SyncBeforeClose(context.Background()); err != nil {
		t.Fatalf("close without pending local changes should not require a token: %v", err)
	}
}

func TestStartupRestoresPendingFirstSyncConfirmation(t *testing.T) {
	store, err := Open(pathJoinTemp(t, "pending-confirmation-restart.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.cloud.close()
	if _, err := store.db.Exec(`UPDATE sync_config SET owner='dingyu', primary_repo='offer-atlas-sync', enabled=1, initial_sync_pending=1, state='syncing' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	store.cloud.checkOnStartup()
	config, err := store.cloud.config()
	if err != nil {
		t.Fatal(err)
	}
	if !config.InitialSyncPending || config.State != "pending_confirmation" {
		t.Fatalf("restart must preserve the first-sync decision, got %#v", config)
	}
	status, err := store.CloudSyncStatus()
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "pending_confirmation" || status.CanSync {
		t.Fatalf("pending confirmation should remain actionable in the UI, got %#v", status)
	}
}

func TestLocalMutationDuringSyncStaysInTheNextBatch(t *testing.T) {
	store, err := Open(pathJoinTemp(t, "next-sync-batch.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.cloud.close()
	if _, err := store.db.Exec(`UPDATE sync_config SET device_id='test-device', owner='dingyu', primary_repo='offer-atlas-sync', enabled=1, initial_sync_pending=0, state='synced' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if err := store.cloud.captureLocalChanges(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE sync_operations SET synced_at=?; UPDATE sync_object_states SET dirty=0, remote_version=object_version; UPDATE sync_config SET state='syncing' WHERE id=1`, nowString()); err != nil {
		t.Fatal(err)
	}
	store.cloud.setActivity("syncing", 0)
	defer store.cloud.clearActivity()

	if _, err := store.SaveCompany(domain.CompanyInput{Name: "Queued During Sync Co."}); err != nil {
		t.Fatal(err)
	}
	status, err := store.CloudSyncStatus()
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "syncing" || status.Activity != "syncing" || status.ActiveChanges != 0 || status.QueuedChanges != 1 {
		t.Fatalf("new edits must remain visible as the next batch, got %#v", status)
	}
	if !strings.Contains(status.Message, "下一批同步") {
		t.Fatalf("status should explain that the edit will sync next, got %q", status.Message)
	}
}

func TestStatusRefreshWithoutChangeDoesNotCreateApplicationSyncOperation(t *testing.T) {
	store, err := Open(pathJoinTemp(t, "stable-application-status.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.cloud.close()

	company, err := store.SaveCompany(domain.CompanyInput{Name: "Status Refresh Co."})
	if err != nil {
		t.Fatal(err)
	}
	campaign, err := store.SaveCampaign(domain.CampaignInput{CompanyID: company.ID, Name: "2027 秋招"})
	if err != nil {
		t.Fatal(err)
	}
	position, err := store.SavePosition(domain.PositionInput{CampaignID: campaign.ID, Title: "Backend", Priority: 3})
	if err != nil {
		t.Fatal(err)
	}
	application, err := store.SaveApplication(domain.ApplicationInput{PositionID: position.ID})
	if err != nil {
		t.Fatal(err)
	}
	stage, err := store.SaveApplicationStage(domain.ApplicationStageInput{
		ApplicationID: application.ID,
		Content:       "一面",
		Type:          domain.StageFirstInterview,
		Status:        domain.StageScheduled,
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := store.db.Exec(`UPDATE sync_config SET device_id='test-device', enabled=1, initial_sync_pending=0, state='synced' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if err := store.cloud.captureLocalChanges(); err != nil {
		t.Fatalf("capture baseline: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE sync_operations SET synced_at=?; UPDATE sync_object_states SET dirty=0, remote_version=object_version`, nowString()); err != nil {
		t.Fatal(err)
	}

	var before string
	if err := store.db.QueryRow(`SELECT updated_at FROM applications WHERE id=?`, application.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	if err := store.refreshAllApplicationStatuses(); err != nil {
		t.Fatalf("refresh unchanged statuses: %v", err)
	}
	var after string
	if err := store.db.QueryRow(`SELECT updated_at FROM applications WHERE id=?`, application.ID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("unchanged status must not rewrite updated_at: before=%s after=%s", before, after)
	}
	if err := store.cloud.captureLocalChanges(); err != nil {
		t.Fatalf("capture after stable refresh: %v", err)
	}
	var pendingApplications int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM sync_operations WHERE synced_at='' AND object_type='application'`).Scan(&pendingApplications); err != nil {
		t.Fatal(err)
	}
	if pendingApplications != 0 {
		t.Fatalf("stable refresh created %d application sync operations", pendingApplications)
	}

	if _, err := store.SaveApplicationStage(domain.ApplicationStageInput{
		ID:            stage.ID,
		ApplicationID: application.ID,
		Content:       stage.Content,
		Type:          stage.Type,
		Status:        domain.StageFailed,
	}); err != nil {
		t.Fatalf("set stage failed: %v", err)
	}
	var status domain.ApplicationStatus
	if err := store.db.QueryRow(`SELECT status FROM applications WHERE id=?`, application.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != domain.ApplicationRejected {
		t.Fatalf("real stage outcome must refresh application status, got %q", status)
	}
	if err := store.cloud.captureLocalChanges(); err != nil {
		t.Fatalf("capture changed status: %v", err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM sync_operations WHERE synced_at='' AND object_type='application'`).Scan(&pendingApplications); err != nil {
		t.Fatal(err)
	}
	if pendingApplications != 1 {
		t.Fatalf("changed status should create one application sync operation, got %d", pendingApplications)
	}
}

func TestSyncCaptureOrdersParentsBeforeChildrenAndDeletesInReverse(t *testing.T) {
	store, err := Open(pathJoinTemp(t, "offer-atlas.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	_, err = store.db.Exec(`UPDATE sync_config SET device_id='test-device', enabled=1, initial_sync_pending=0, state='pending' WHERE id=1`)
	if err != nil {
		t.Fatal(err)
	}
	company, _ := store.SaveCompany(domain.CompanyInput{Name: "Ordering Co"})
	campaign, _ := store.SaveCampaign(domain.CampaignInput{CompanyID: company.ID, Name: "2027 秋招"})
	position, _ := store.SavePosition(domain.PositionInput{CampaignID: campaign.ID, Title: "Backend", Priority: 3})
	application, _ := store.SaveApplication(domain.ApplicationInput{PositionID: position.ID, SubmittedOn: "2026-08-21"})
	stage, _ := store.SaveApplicationStage(domain.ApplicationStageInput{ApplicationID: application.ID, Content: "笔试", Type: domain.StageWrittenTest, Status: domain.StageScheduled})
	if err := store.cloud.captureLocalChanges(); err != nil {
		t.Fatalf("capture: %v", err)
	}
	var captured []string
	rows, err := store.db.Query(`SELECT object_type FROM sync_operations ORDER BY sequence`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatal(err)
		}
		captured = append(captured, value)
	}
	rows.Close()
	firstIndex := map[string]int{}
	for index, value := range captured {
		if _, known := firstIndex[value]; !known {
			firstIndex[value] = index
		}
	}
	wantOrder := []string{"company", "campaign", "position", "application", "stage"}
	for index := 1; index < len(wantOrder); index++ {
		if firstIndex[wantOrder[index-1]] >= firstIndex[wantOrder[index]] {
			t.Fatalf("parent capture order is not stable: %v", captured)
		}
	}
	if err := store.DeleteApplicationStage(stage.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteEntity(domain.DeleteInput{EntityType: domain.DeletionTargetApplication, ID: application.ID, ConfirmationText: "Backend"}); err == nil {
		// The confirmation is intentionally exact and differs by label in normal use.
		// Retain the test database even when the UI confirmation format changes.
	}
}

func TestGiteeSyncRestoresAttachmentAndResumeThroughMediaIndex(t *testing.T) {
	server := newFakeGitee(t)
	defer server.Close()
	first, err := Open(pathJoinTemp(t, "first.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	first.cloud.clientFactory = func(token string) *giteeClient { return newGiteeClient(token).withBaseURL(server.URL() + "/api/v5") }
	company, _ := first.SaveCompany(domain.CompanyInput{Name: "Media Co"})
	campaign, _ := first.SaveCampaign(domain.CampaignInput{CompanyID: company.ID, Name: "秋招"})
	position, _ := first.SavePosition(domain.PositionInput{CampaignID: campaign.ID, Title: "Go 开发", Priority: 4})
	application, _ := first.SaveApplication(domain.ApplicationInput{PositionID: position.ID, SubmittedOn: "2026-08-21"})
	if _, err := first.ImportPositionAttachmentData(position.ID, "JD.png", "image/png", []byte("position-attachment-contents")); err != nil {
		t.Fatal(err)
	}
	if _, err := first.SaveResourceLinks(domain.ResourceOwnerApplication, application.ID, []domain.ResourceLinkInput{{Name: "投递流程", URL: "https://jobs.example.com/flow"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := first.ImportSupplementalAttachmentData(domain.ResourceOwnerApplication, application.ID, "notice.png", "image/png", []byte("supplemental-attachment-contents")); err != nil {
		t.Fatal(err)
	}
	if _, err := first.ImportApplicationResumeData(application.ID, "resume.pdf", "application/pdf", []byte("resume-contents")); err != nil {
		t.Fatal(err)
	}
	preview, err := first.ConnectGitee(context.Background(), "same-token")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.ConfirmGiteeConnection(context.Background(), "upload"); err != nil {
		t.Fatal(err)
	}
	if server.countPrefix(preview.PrimaryRepo, "media-index/") != 3 || server.countPrefix("offer-atlas-media-001", "media/") != 3 {
		t.Fatal("attachments were not indexed and stored in the media repository")
	}

	second, err := Open(pathJoinTemp(t, "second.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	second.cloud.clientFactory = func(token string) *giteeClient { return newGiteeClient(token).withBaseURL(server.URL() + "/api/v5") }
	secondPreview, err := second.ConnectGitee(context.Background(), "same-token")
	if err != nil {
		t.Fatal(err)
	}
	if secondPreview.Recommended != "download" {
		t.Fatalf("expected cloud download for empty device: %#v", secondPreview)
	}
	status, err := second.ConfirmGiteeConnection(context.Background(), "download")
	if err != nil {
		t.Fatal(err)
	}
	if status.ConflictCount != 0 || status.PendingChanges != 0 {
		t.Fatalf("empty-device download must not create local conflicts or pending edits: %#v", status)
	}
	detail, err := second.GetPositionDetail(position.ID)
	if err != nil || len(detail.Attachments) != 1 || detail.Resume == nil {
		t.Fatalf("restored media metadata: %#v, %v", detail, err)
	}
	attachmentPath, err := second.PositionAttachmentPath(detail.Attachments[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if contents, _ := os.ReadFile(attachmentPath); string(contents) != "position-attachment-contents" {
		t.Fatalf("attachment content did not restore: %q", contents)
	}
	resumePath, err := second.ApplicationResumePath(application.ID)
	if err != nil {
		t.Fatal(err)
	}
	if contents, _ := os.ReadFile(resumePath); string(contents) != "resume-contents" {
		t.Fatalf("resume content did not restore: %q", contents)
	}
	applicationDetail, err := second.GetApplicationDetail(application.ID)
	if err != nil || len(applicationDetail.Links) != 1 || applicationDetail.Links[0].Name != "投递流程" || len(applicationDetail.Attachments) != 1 {
		t.Fatalf("supplemental metadata did not restore: %#v, %v", applicationDetail, err)
	}
	supplementalPath, err := second.SupplementalAttachmentPath(applicationDetail.Attachments[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if contents, _ := os.ReadFile(supplementalPath); string(contents) != "supplemental-attachment-contents" {
		t.Fatalf("supplemental attachment content did not restore: %q", contents)
	}
	if server.operationReadCount() == 0 {
		t.Fatal("initial download should read remote operation contents")
	}
	server.resetOperationReads()
	if _, err := second.SyncGiteeNow(context.Background()); err != nil {
		t.Fatalf("stable incremental refresh: %v", err)
	}
	if reads := server.operationReadCount(); reads != 0 {
		t.Fatalf("stable refresh re-read %d historical operation files; expected cursor-based incremental read", reads)
	}
}

func TestGiteeSyncRecoversWhenMediaIndexPointsToDirectory(t *testing.T) {
	server := newFakeGitee(t)
	defer server.Close()
	first, err := Open(pathJoinTemp(t, "stale-media-index-first.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	first.cloud.clientFactory = func(token string) *giteeClient { return newGiteeClient(token).withBaseURL(server.URL() + "/api/v5") }
	company, _ := first.SaveCompany(domain.CompanyInput{Name: "Stale Index Co"})
	campaign, _ := first.SaveCampaign(domain.CampaignInput{CompanyID: company.ID, Name: "秋招"})
	position, _ := first.SavePosition(domain.PositionInput{CampaignID: campaign.ID, Title: "后端开发", Priority: 3})
	if _, err := first.ImportPositionAttachmentData(position.ID, "JD.png", "image/png", []byte("stale-index-attachment")); err != nil {
		t.Fatal(err)
	}
	preview, err := first.ConnectGitee(context.Background(), "same-token")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.ConfirmGiteeConnection(context.Background(), "upload"); err != nil {
		t.Fatal(err)
	}

	// Reproduce an older repository entry that stores the media directory rather
	// than the file path. The reader receives a directory listing (an array).
	server.mu.Lock()
	for file, data := range server.repos[preview.PrimaryRepo] {
		if !strings.HasPrefix(file, "media-index/") {
			continue
		}
		var entry map[string]any
		if json.Unmarshal(data, &entry) != nil {
			continue
		}
		hash := strings.TrimSuffix(strings.TrimPrefix(file, "media-index/"), ".json")
		entry["remotePath"] = "media/" + hash[:2]
		server.repos[preview.PrimaryRepo][file], _ = json.Marshal(entry)
		break
	}
	server.mu.Unlock()

	second, err := Open(pathJoinTemp(t, "stale-media-index-second.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	second.cloud.clientFactory = func(token string) *giteeClient { return newGiteeClient(token).withBaseURL(server.URL() + "/api/v5") }
	if _, err := second.ConnectGitee(context.Background(), "same-token"); err != nil {
		t.Fatal(err)
	}
	if _, err := second.ConfirmGiteeConnection(context.Background(), "download"); err != nil {
		t.Fatalf("stale media index must be recovered: %v", err)
	}
	attachments, err := second.ListPositionAttachments(position.ID)
	if err != nil || len(attachments) != 1 {
		t.Fatalf("restored attachment metadata: %#v, %v", attachments, err)
	}
	attachmentPath, err := second.PositionAttachmentPath(attachments[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(attachmentPath)
	if err != nil || string(contents) != "stale-index-attachment" {
		t.Fatalf("stale media index did not recover attachment: %q, %v", contents, err)
	}
}

func TestGiteeSyncAppliesResumeBindingAddedAfterInitialDownload(t *testing.T) {
	server := newFakeGitee(t)
	defer server.Close()
	configureClient := func(store *Store) {
		store.cloud.clientFactory = func(token string) *giteeClient {
			return newGiteeClient(token).withBaseURL(server.URL() + "/api/v5")
		}
	}
	first, err := Open(pathJoinTemp(t, "resume-binding-first.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	configureClient(first)
	company, _ := first.SaveCompany(domain.CompanyInput{Name: "Resume Binding Co"})
	campaign, _ := first.SaveCampaign(domain.CampaignInput{CompanyID: company.ID, Name: "2027 秋招"})
	position, _ := first.SavePosition(domain.PositionInput{CampaignID: campaign.ID, Title: "后端开发", Priority: 4})
	application, _ := first.SaveApplication(domain.ApplicationInput{PositionID: position.ID, SubmittedOn: "2026-08-22"})
	_, err = first.ConnectGitee(context.Background(), "same-token")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.ConfirmGiteeConnection(context.Background(), "upload"); err != nil {
		t.Fatal(err)
	}

	second, err := Open(pathJoinTemp(t, "resume-binding-second.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	configureClient(second)
	if _, err := second.ConnectGitee(context.Background(), "same-token"); err != nil {
		t.Fatal(err)
	}
	if _, err := second.ConfirmGiteeConnection(context.Background(), "download"); err != nil {
		t.Fatal(err)
	}
	before, err := second.GetApplicationDetail(application.ID)
	if err != nil || before.Resume != nil || before.Application.ResumeID != "" {
		t.Fatalf("target should initially have the unbound application: %#v, %v", before, err)
	}

	resume, err := first.ImportResumeData("后端开发 v4", "backend-v4.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", []byte("resume-v4"))
	if err != nil {
		t.Fatal(err)
	}
	if err := first.SetApplicationResume(application.ID, resume.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := first.SyncGiteeNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	server.resetOperationReads()
	if _, err := second.SyncGiteeNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if reads := server.operationReadCount(); reads != 2 {
		t.Fatalf("incremental download should read only the new resume and application operations, got %d reads", reads)
	}

	after, err := second.GetApplicationDetail(application.ID)
	if err != nil || after.Application.ResumeID != resume.ID || after.Resume == nil || after.Resume.ID != resume.ID {
		t.Fatalf("subsequent resume binding was not applied to the target: %#v, %v", after, err)
	}
	resumes, err := second.ListResumes(true)
	if err != nil || len(resumes) != 1 || resumes[0].ID != resume.ID || resumes[0].UsageCount != 1 {
		t.Fatalf("resume library must show the synchronized application association: %#v, %v", resumes, err)
	}
	path, err := second.ResumePath(resume.ID)
	if err != nil {
		t.Fatal(err)
	}
	if contents, readErr := os.ReadFile(path); readErr != nil || string(contents) != "resume-v4" {
		t.Fatalf("bound resume file was not restored: %q, %v", contents, readErr)
	}
}

func TestSelectRemoteOperationFilesDoesNotSkipMissingSequence(t *testing.T) {
	files := []giteeContent{
		{Name: "00000000000000000001.json", Path: "operations/device/00000000000000000001.json", Type: "file"},
		{Name: "00000000000000000003.json", Path: "operations/device/00000000000000000003.json", Type: "file"},
	}
	selected := selectRemoteOperationFiles(files, 0)
	if len(selected) != 1 || selected[0].sequence != 1 {
		t.Fatalf("a gap must stop incremental application instead of skipping to a later operation: %#v", selected)
	}
}

func TestCompactSupersededPendingOperationsSkipsDeletedAttachmentUpload(t *testing.T) {
	operations := []syncOperation{
		{ID: "resume-upsert", ObjectType: "resume", ObjectID: "resume-1", Action: "upsert", Sequence: 189},
		{ID: "resume-delete", ObjectType: "resume", ObjectID: "resume-1", Action: "delete", Sequence: 190},
		{ID: "company-upsert", ObjectType: "company", ObjectID: "company-1", Action: "upsert", Sequence: 191},
	}
	kept := compactSupersededPendingOperations(operations)
	if len(kept) != 3 || kept[0].ID != "resume-upsert" || kept[0].Action != "noop" || kept[1].ID != "resume-delete" || kept[2].ID != "company-upsert" {
		t.Fatalf("obsolete attachment operation must retain its sequence as a no-op: %#v", kept)
	}
}

func TestNormalizeRemoteResumePayloadRestoresHistoricalFilename(t *testing.T) {
	payload, err := normalizeRemoteFilePayload("resume", json.RawMessage(`{"id":"resume-1","stored_name":"","content_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","mime_type":"application/vnd.openxmlformats-officedocument.wordprocessingml.document"}`))
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(payload, &value); err != nil {
		t.Fatal(err)
	}
	if value["stored_name"] != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.docx" {
		t.Fatalf("historical resume filename was not derived from its hash: %#v", value["stored_name"])
	}
}

func TestUploadPendingDoesNotReadAttachmentSupersededByDelete(t *testing.T) {
	server := newFakeGitee(t)
	defer server.Close()
	server.mu.Lock()
	server.repos["offer-atlas-sync"] = map[string][]byte{}
	server.mu.Unlock()
	store, err := Open(pathJoinTemp(t, "superseded-upload.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.cloud.close()
	if _, err := store.db.Exec(`UPDATE sync_config SET device_id='device-a', owner='dingyu', primary_repo='offer-atlas-sync', enabled=1, initial_sync_pending=0, state='pending', next_sequence=3 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	now := nowString()
	upsertPayload := `{"id":"resume-1","name":"deleted resume","original_name":"deleted.docx","stored_name":"missing.docx","mime_type":"application/vnd.openxmlformats-officedocument.wordprocessingml.document","size_bytes":10,"content_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","archived":0,"created_at":"` + now + `","updated_at":"` + now + `"}`
	deletePayload := upsertPayload
	for _, operation := range []syncOperation{
		{ID: "stale-upsert", DeviceID: "device-a", Sequence: 1, ObjectType: "resume", ObjectID: "resume-1", Action: "upsert", ObjectVersion: 1, BaseVersion: 0, Payload: json.RawMessage(upsertPayload), CreatedAt: now},
		{ID: "final-delete", DeviceID: "device-a", Sequence: 2, ObjectType: "resume", ObjectID: "resume-1", Action: "delete", ObjectVersion: 2, BaseVersion: 1, Payload: json.RawMessage(deletePayload), CreatedAt: now},
	} {
		if _, err := store.db.Exec(`INSERT INTO sync_operations(id, device_id, sequence, object_type, object_id, action, object_version, base_version, payload, created_at, synced_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '')`, operation.ID, operation.DeviceID, operation.Sequence, operation.ObjectType, operation.ObjectID, operation.Action, operation.ObjectVersion, operation.BaseVersion, string(operation.Payload), operation.CreatedAt); err != nil {
			t.Fatal(err)
		}
	}
	config, err := store.cloud.config()
	if err != nil {
		t.Fatal(err)
	}
	client := newGiteeClient("same-token").withBaseURL(server.URL() + "/api/v5")
	if err := store.cloud.uploadPending(context.Background(), client, config, 2); err != nil {
		t.Fatalf("superseded missing attachment must not block delete upload: %v", err)
	}
	if !server.hasFile("offer-atlas-sync", "operations/device-a/00000000000000000001.json") {
		t.Fatal("obsolete sequence must be represented in the remote log")
	}
	if !server.hasFile("offer-atlas-sync", "operations/device-a/00000000000000000002.json") {
		t.Fatal("final delete was not uploaded")
	}
	var pending int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM sync_operations WHERE synced_at=''`).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("superseded and final operations should be checkpointed, pending=%d err=%v", pending, err)
	}
	data, _, err := client.readFile(context.Background(), "dingyu", "offer-atlas-sync", "operations/device-a/00000000000000000001.json")
	if err != nil {
		t.Fatal(err)
	}
	var remote syncOperation
	if err := json.Unmarshal(data, &remote); err != nil || remote.Action != "noop" {
		t.Fatalf("superseded operation must be uploaded as a no-op, operation=%#v err=%v", remote, err)
	}
}

func TestUploadPendingStopsAtOpenConflictWithoutCreatingLogGap(t *testing.T) {
	server := newFakeGitee(t)
	defer server.Close()
	server.mu.Lock()
	server.repos["offer-atlas-sync"] = map[string][]byte{}
	server.mu.Unlock()
	store, err := Open(pathJoinTemp(t, "blocked-conflict-upload.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.cloud.close()
	if _, err := store.db.Exec(`UPDATE sync_config SET device_id='device-a', owner='dingyu', primary_repo='offer-atlas-sync', enabled=1, initial_sync_pending=0, state='pending', next_sequence=3 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	now := nowString()
	for _, operation := range []syncOperation{
		{ID: "conflicted-change", DeviceID: "device-a", Sequence: 1, ObjectType: "company", ObjectID: "company-1", Action: "upsert", ObjectVersion: 2, BaseVersion: 1, Payload: json.RawMessage(`{"id":"company-1","name":"local"}`), CreatedAt: now},
		{ID: "later-change", DeviceID: "device-a", Sequence: 2, ObjectType: "company", ObjectID: "company-2", Action: "upsert", ObjectVersion: 1, BaseVersion: 0, Payload: json.RawMessage(`{"id":"company-2","name":"later"}`), CreatedAt: now},
	} {
		if _, err := store.db.Exec(`INSERT INTO sync_operations(id, device_id, sequence, object_type, object_id, action, object_version, base_version, payload, created_at, synced_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '')`, operation.ID, operation.DeviceID, operation.Sequence, operation.ObjectType, operation.ObjectID, operation.Action, operation.ObjectVersion, operation.BaseVersion, string(operation.Payload), operation.CreatedAt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.Exec(`INSERT INTO sync_conflicts(id, object_type, object_id, local_payload, remote_payload, local_updated_at, remote_updated_at, status, created_at) VALUES ('conflict-1','company','company-1','{}','{}',?,?, 'open',?)`, now, now, now); err != nil {
		t.Fatal(err)
	}
	config, err := store.cloud.config()
	if err != nil {
		t.Fatal(err)
	}
	client := newGiteeClient("same-token").withBaseURL(server.URL() + "/api/v5")
	if err := store.cloud.uploadPending(context.Background(), client, config, 2); err != nil {
		t.Fatalf("blocked conflict should wait without failing: %v", err)
	}
	if server.hasFile("offer-atlas-sync", "operations/device-a/00000000000000000001.json") || server.hasFile("offer-atlas-sync", "operations/device-a/00000000000000000002.json") {
		t.Fatal("an open conflict must not upload a later branch or create a sequence gap")
	}
	var pending int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM sync_operations WHERE synced_at=''`).Scan(&pending); err != nil || pending != 2 {
		t.Fatalf("conflicted operations must remain pending, pending=%d err=%v", pending, err)
	}
}

func TestGiteePullAcceptsNoopSequenceBeforeDelete(t *testing.T) {
	server := newFakeGitee(t)
	defer server.Close()
	server.mu.Lock()
	server.repos["offer-atlas-sync"] = map[string][]byte{}
	for sequence, action := range []string{"noop", "delete"} {
		operation, _ := json.Marshal(syncOperation{
			ID: fmt.Sprintf("remote-operation-%d", sequence+1), DeviceID: "source-device", Sequence: int64(sequence + 1),
			ObjectType: "resume", ObjectID: "resume-created-and-deleted", Action: action,
			ObjectVersion: int64(sequence + 1), BaseVersion: int64(sequence), Payload: json.RawMessage(`{}`), CreatedAt: nowString(),
		})
		remotePath := fmt.Sprintf("operations/source-device/%020d.json", sequence+1)
		server.repos["offer-atlas-sync"][remotePath] = operation
	}
	server.mu.Unlock()

	target, err := Open(pathJoinTemp(t, "noop-pull.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	target.cloud.close()
	config := syncConfigRow{DeviceID: "target-device", Owner: "dingyu", PrimaryRepo: "offer-atlas-sync", Enabled: true}
	client := newGiteeClient("same-token").withBaseURL(server.URL() + "/api/v5")
	if err := target.cloud.pullRemote(context.Background(), client, config); err != nil {
		t.Fatalf("pulling a contiguous no-op/delete log must succeed: %v", err)
	}
	var cursor int64
	if err := target.db.QueryRow(`SELECT last_sequence FROM sync_remote_cursors WHERE device_id='source-device'`).Scan(&cursor); err != nil || cursor != 2 {
		t.Fatalf("no-op and delete sequences should advance the cursor together: cursor=%d err=%v", cursor, err)
	}
}

func TestGiteeFirstDownloadResumesFromDurableOperationCheckpoint(t *testing.T) {
	server := newFakeGitee(t)
	defer server.Close()
	server.mu.Lock()
	server.repos["offer-atlas-sync"] = map[string][]byte{}
	for sequence, id := range []string{"company-one", "company-two"} {
		payload, _ := json.Marshal(map[string]any{
			"id": id, "name": id, "industry": "", "homepage": "", "notes": "",
			"created_at": nowString(), "updated_at": nowString(),
		})
		operation, _ := json.Marshal(syncOperation{ID: fmt.Sprintf("operation-%d", sequence+1), DeviceID: "source-device", Sequence: int64(sequence + 1), ObjectType: "company", ObjectID: id, Action: "upsert", ObjectVersion: 1, BaseVersion: 0, Payload: payload, CreatedAt: nowString()})
		remotePath := fmt.Sprintf("operations/source-device/%020d-operation-%d.json", sequence+1, sequence+1)
		server.repos["offer-atlas-sync"][remotePath] = operation
	}
	server.mu.Unlock()

	target, err := Open(pathJoinTemp(t, "checkpoint-target.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	target.cloud.close()
	config := syncConfigRow{DeviceID: "target-device", Owner: "dingyu", PrimaryRepo: "offer-atlas-sync", Enabled: true}
	client := newGiteeClient("same-token").withBaseURL(server.URL() + "/api/v5")
	failingPath := "operations/source-device/00000000000000000002-operation-2.json"
	server.failReadOnce("offer-atlas-sync", failingPath)
	if err := target.cloud.pullRemote(context.Background(), client, config); err == nil {
		t.Fatal("expected a temporary failure while reading the second remote operation")
	}
	if _, err := target.companyByID("company-one"); err != nil {
		t.Fatalf("the first operation must be committed before a later read fails: %v", err)
	}
	var cursor int64
	if err := target.db.QueryRow(`SELECT last_sequence FROM sync_remote_cursors WHERE device_id='source-device'`).Scan(&cursor); err != nil || cursor != 1 {
		t.Fatalf("first failure must persist a cursor after the completed operation: cursor=%d err=%v", cursor, err)
	}

	target.cloud.clearActivity()
	server.resetOperationReads()
	if err := target.cloud.pullRemote(context.Background(), client, config); err != nil {
		t.Fatalf("resumed remote download: %v", err)
	}
	if reads := server.operationReadCount(); reads != 1 {
		t.Fatalf("resume must read only the failed remainder, got %d operation reads", reads)
	}
	if _, err := target.companyByID("company-two"); err != nil {
		t.Fatalf("second operation was not imported after resume: %v", err)
	}
	if err := target.db.QueryRow(`SELECT last_sequence FROM sync_remote_cursors WHERE device_id='source-device'`).Scan(&cursor); err != nil || cursor != 2 {
		t.Fatalf("resumed download must advance final cursor: cursor=%d err=%v", cursor, err)
	}
}

func TestGiteeFirstDownloadDefersChildrenUntilRemoteParentsArrive(t *testing.T) {
	server := newFakeGitee(t)
	defer server.Close()
	server.mu.Lock()
	server.repos["offer-atlas-sync"] = map[string][]byte{}
	now := nowString()
	childPayload, _ := json.Marshal(map[string]any{
		"id": "campaign-child", "company_id": "company-parent", "name": "2027 秋招",
		"opens_on": nil, "closes_on": nil, "source_url": "", "last_verified_on": nil,
		"process_overview": "", "notes": "", "created_at": now, "updated_at": now,
	})
	parentPayload, _ := json.Marshal(map[string]any{
		"id": "company-parent", "name": "Parent Company", "industry": "", "homepage": "", "notes": "",
		"created_at": now, "updated_at": now,
	})
	childOperation, _ := json.Marshal(syncOperation{
		ID: "child-operation", DeviceID: "a-child", Sequence: 1, ObjectType: "campaign", ObjectID: "campaign-child",
		Action: "upsert", ObjectVersion: 1, BaseVersion: 0, Payload: childPayload, CreatedAt: now,
	})
	parentOperation, _ := json.Marshal(syncOperation{
		ID: "parent-operation", DeviceID: "z-parent", Sequence: 1, ObjectType: "company", ObjectID: "company-parent",
		Action: "upsert", ObjectVersion: 1, BaseVersion: 0, Payload: parentPayload, CreatedAt: now,
	})
	server.repos["offer-atlas-sync"]["operations/a-child/00000000000000000001-child.json"] = childOperation
	server.repos["offer-atlas-sync"]["operations/z-parent/00000000000000000001-parent.json"] = parentOperation
	server.mu.Unlock()

	target, err := Open(pathJoinTemp(t, "deferred-dependency-target.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	target.cloud.close()
	config := syncConfigRow{DeviceID: "target-device", Owner: "dingyu", PrimaryRepo: "offer-atlas-sync", Enabled: true}
	client := newGiteeClient("same-token").withBaseURL(server.URL() + "/api/v5")
	if err := target.cloud.pullRemote(context.Background(), client, config); err != nil {
		t.Fatalf("child operation should wait for its remote parent: %v", err)
	}
	if _, err := target.companyByID("company-parent"); err != nil {
		t.Fatalf("parent company was not imported: %v", err)
	}
	campaign, err := target.campaignByID("campaign-child")
	if err != nil || campaign.CompanyID != "company-parent" {
		t.Fatalf("deferred child was not imported after its parent: %#v, %v", campaign, err)
	}
	for _, deviceID := range []string{"a-child", "z-parent"} {
		var cursor int64
		if err := target.db.QueryRow(`SELECT last_sequence FROM sync_remote_cursors WHERE device_id=?`, deviceID).Scan(&cursor); err != nil || cursor != 1 {
			t.Fatalf("cursor for %s was not advanced after deferred import: cursor=%d err=%v", deviceID, cursor, err)
		}
	}
}

func TestGiteeSequentialMergeAndVersionForkConflict(t *testing.T) {
	store, err := Open(pathJoinTemp(t, "offer-atlas.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.db.Exec(`UPDATE sync_config SET device_id='device-a', owner='dingyu', primary_repo='offer-atlas-sync', enabled=1, initial_sync_pending=0, state='pending' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	company, err := store.SaveCompany(domain.CompanyInput{Name: "Initial Company"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.cloud.captureLocalChanges(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE sync_object_states SET dirty=0, remote_version=1 WHERE object_type='company' AND object_id=?; UPDATE sync_operations SET synced_at=?`, company.ID, nowString()); err != nil {
		t.Fatal(err)
	}
	remotePayload := map[string]any{"id": company.ID, "name": "Cloud Sequential", "industry": "", "homepage": "", "notes": "", "created_at": nowString(), "updated_at": nowString()}
	payload, _ := json.Marshal(remotePayload)
	config, _ := store.cloud.config()
	remote := syncOperation{ID: "remote-sequential", DeviceID: "device-b", Sequence: 1, ObjectType: "company", ObjectID: company.ID, Action: "upsert", ObjectVersion: 2, BaseVersion: 1, Payload: payload, CreatedAt: nowString()}
	if err := store.cloud.applyRemoteOperation(context.Background(), nil, config, remote); err != nil {
		t.Fatalf("sequential remote apply: %v", err)
	}
	merged, err := store.companyByID(company.ID)
	if err != nil || merged.Name != "Cloud Sequential" {
		t.Fatalf("remote sequential change was not applied: %#v, %v", merged, err)
	}
	if _, err := store.SaveCompany(domain.CompanyInput{ID: company.ID, Name: "Local Fork"}); err != nil {
		t.Fatal(err)
	}
	remotePayload["name"] = "Cloud Fork"
	payload, _ = json.Marshal(remotePayload)
	remote = syncOperation{ID: "remote-fork", DeviceID: "device-b", Sequence: 2, ObjectType: "company", ObjectID: company.ID, Action: "upsert", ObjectVersion: 3, BaseVersion: 2, Payload: payload, CreatedAt: nowString()}
	if err := store.cloud.applyRemoteOperation(context.Background(), nil, config, remote); err != nil {
		t.Fatalf("fork detection: %v", err)
	}
	conflicts, err := store.ListSyncConflicts()
	if err != nil || len(conflicts) != 1 || conflicts[0].LocalDescription != "Local Fork" || conflicts[0].RemoteDescription != "Cloud Fork" {
		t.Fatalf("expected descriptive version-fork conflict: %#v, %v", conflicts, err)
	}
}

func TestCloudSyncCompatibilityGateBlocksTransferBeforePullAndUpload(t *testing.T) {
	server := newFakeGitee(t)
	defer server.Close()

	source, err := Open(pathJoinTemp(t, "compat-source.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	source.cloud.clientFactory = func(token string) *giteeClient {
		return newGiteeClient(token).withBaseURL(server.URL() + "/api/v5")
	}
	if _, err := source.SaveCompany(domain.CompanyInput{Name: "Compatibility Source"}); err != nil {
		t.Fatal(err)
	}
	_, err = source.ConnectGitee(context.Background(), "same-token")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.ConfirmGiteeConnection(context.Background(), "upload"); err != nil {
		t.Fatal(err)
	}

	target, err := Open(pathJoinTemp(t, "compat-target.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	target.cloud.clientFactory = func(token string) *giteeClient {
		return newGiteeClient(token).withBaseURL(server.URL() + "/api/v5")
	}
	preview, err := target.ConnectGitee(context.Background(), "same-token")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := target.ConfirmGiteeConnection(context.Background(), "download"); err != nil {
		t.Fatal(err)
	}

	marker, err := json.Marshal(syncRepositoryMarker{
		Product: "Offer Atlas", Format: syncFormatVersion, Kind: "primary", CreatedAt: nowString(),
		MinimumClientVersion: "9.0.0", RequiredCapabilities: []string{"future-sync-v1"}, CompatibilityEpoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	server.mu.Lock()
	server.repos[preview.PrimaryRepo][syncFormatPath] = marker
	server.mu.Unlock()
	if _, err := target.SaveCompany(domain.CompanyInput{Name: "Blocked Local Change"}); err != nil {
		t.Fatal(err)
	}
	beforeFiles := server.countPrefix(preview.PrimaryRepo, "operations/")
	server.resetOperationReads()
	if _, err := target.SyncGiteeNow(context.Background()); err == nil {
		t.Fatal("expected cloud compatibility gate to block the sync")
	}
	status, err := target.CloudSyncStatus()
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "update_required" || status.CanSync || status.MinimumClientVersion != "9.0.0" {
		t.Fatalf("unexpected compatibility-blocked status: %#v", status)
	}
	if reads := server.operationReadCount(); reads != 0 {
		t.Fatalf("compatibility gate must run before reading operation files, got %d reads", reads)
	}
	if afterFiles := server.countPrefix(preview.PrimaryRepo, "operations/"); afterFiles != beforeFiles {
		t.Fatalf("compatibility gate must run before upload, files before=%d after=%d", beforeFiles, afterFiles)
	}
}

func TestCloudSyncCompatibilityCacheNeverAuthorizesOfflineTransfer(t *testing.T) {
	server := newFakeGitee(t)
	defer server.Close()

	store, err := Open(pathJoinTemp(t, "compat-cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.cloud.clientFactory = func(token string) *giteeClient {
		return newGiteeClient(token).withBaseURL(server.URL() + "/api/v5")
	}
	if _, err := store.SaveCompany(domain.CompanyInput{Name: "Cache Guard Co."}); err != nil {
		t.Fatal(err)
	}
	preview, err := store.ConnectGitee(context.Background(), "same-token")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConfirmGiteeConnection(context.Background(), "upload"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.cloud.cachedRemoteCompatibility(); err != nil {
		t.Fatalf("read compatibility cache: %v", err)
	}
	if _, err := store.SaveCompany(domain.CompanyInput{Name: "Cache Guard Pending"}); err != nil {
		t.Fatal(err)
	}
	beforeFiles := server.countPrefix(preview.PrimaryRepo, "operations/")
	server.resetOperationReads()
	server.failReadOnce(preview.PrimaryRepo, syncFormatPath)
	config, err := store.cloud.config()
	if err != nil {
		t.Fatal(err)
	}
	cutoff, err := store.cloud.pendingSequenceCutoff()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.cloud.syncOnce(context.Background(), config, cutoff); err == nil {
		t.Fatal("expected marker read failure")
	} else {
		var compatibilityErr *compatibilityCheckError
		if !errors.As(err, &compatibilityErr) {
			t.Fatalf("expected compatibility check error, got %v", err)
		}
	}
	if reads := server.operationReadCount(); reads != 0 {
		t.Fatalf("cached compatibility must not authorize an offline pull, got %d reads", reads)
	}
	if afterFiles := server.countPrefix(preview.PrimaryRepo, "operations/"); afterFiles != beforeFiles {
		t.Fatalf("cached compatibility must not authorize an offline upload, files before=%d after=%d", beforeFiles, afterFiles)
	}
}

func pathJoinTemp(t *testing.T, name string) string { return path.Join(t.TempDir(), name) }

// OS helpers are variables only to keep this test focused on sync behavior.
var osReadFile = func(name string) ([]byte, error) { return os.ReadFile(name) }
var osStat = func(name string) (os.FileInfo, error) { return os.Stat(name) }

type fakeGitee struct {
	t                     *testing.T
	mu                    sync.Mutex
	repos                 map[string]map[string][]byte
	operationReads        int
	failReadPaths         map[string]int
	failNextMarkerCreates int
	server                *httptest.Server
}

func newFakeGitee(t *testing.T) *fakeGitee {
	fake := &fakeGitee{t: t, repos: map[string]map[string][]byte{}, failReadPaths: map[string]int{}}
	fake.server = httptest.NewServer(http.HandlerFunc(fake.serveHTTP))
	return fake
}

func (f *fakeGitee) Close()      { f.server.Close() }
func (f *fakeGitee) URL() string { return f.server.URL }

func (f *fakeGitee) hasFile(repo, remotePath string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.repos[repo][remotePath]
	return ok
}

func (f *fakeGitee) hasRepo(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.repos[name]
	return ok
}

func (f *fakeGitee) repoNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	names := make([]string, 0, len(f.repos))
	for name := range f.repos {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (f *fakeGitee) repoCountWithPrefix(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for name := range f.repos {
		if strings.HasPrefix(name, prefix) {
			count++
		}
	}
	return count
}

func (f *fakeGitee) countPrefix(repo, prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for file := range f.repos[repo] {
		if strings.HasPrefix(file, prefix) {
			count++
		}
	}
	return count
}

func (f *fakeGitee) resetOperationReads() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	previous := f.operationReads
	f.operationReads = 0
	return previous
}

func (f *fakeGitee) operationReadCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.operationReads
}

func (f *fakeGitee) failReadOnce(repo, remotePath string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failReadPaths[repo+":"+remotePath]++
}

func (f *fakeGitee) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	endpoint := strings.TrimPrefix(request.URL.Path, "/api/v5")
	if endpoint == "/user" && request.Method == http.MethodGet {
		writeFakeJSON(writer, map[string]any{"login": "dingyu", "name": "DingYu"})
		return
	}
	if endpoint == "/user/repos" && request.Method == http.MethodGet {
		f.mu.Lock()
		names := make([]string, 0, len(f.repos))
		for name := range f.repos {
			names = append(names, name)
		}
		sort.Strings(names)
		items := make([]map[string]any, 0, len(names))
		for _, name := range names {
			items = append(items, map[string]any{"name": name, "path": name, "private": true, "owner": map[string]any{"login": "dingyu"}, "default_branch": "master"})
		}
		f.mu.Unlock()
		writeFakeJSON(writer, items)
		return
	}
	if endpoint == "/user/repos" && request.Method == http.MethodPost {
		var body struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(request.Body).Decode(&body)
		f.mu.Lock()
		if _, exists := f.repos[body.Name]; exists {
			f.mu.Unlock()
			http.Error(writer, `{"message":"exists"}`, http.StatusUnprocessableEntity)
			return
		}
		f.repos[body.Name] = map[string][]byte{}
		f.mu.Unlock()
		writeFakeJSON(writer, map[string]any{"name": body.Name, "path": body.Name, "private": true, "owner": map[string]any{"login": "dingyu"}, "default_branch": "master"})
		return
	}
	if strings.HasPrefix(endpoint, "/repos/dingyu/") && request.Method == http.MethodDelete {
		repo := strings.TrimPrefix(endpoint, "/repos/dingyu/")
		if strings.Contains(repo, "/") || strings.TrimSpace(repo) == "" {
			http.Error(writer, `{"message":"not found"}`, http.StatusNotFound)
			return
		}
		f.mu.Lock()
		if _, exists := f.repos[repo]; !exists {
			f.mu.Unlock()
			http.Error(writer, `{"message":"not found"}`, http.StatusNotFound)
			return
		}
		delete(f.repos, repo)
		f.mu.Unlock()
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	parts := strings.Split(strings.TrimPrefix(endpoint, "/repos/dingyu/"), "/contents/")
	if len(parts) == 2 {
		repo, remotePath := parts[0], parts[1]
		f.mu.Lock()
		files, exists := f.repos[repo]
		if !exists {
			f.mu.Unlock()
			http.Error(writer, `{"message":"not found"}`, 404)
			return
		}
		if request.Method == http.MethodPost || request.Method == http.MethodPut {
			var body struct {
				SHA     string `json:"sha"`
				Content string `json:"content"`
			}
			_ = json.NewDecoder(request.Body).Decode(&body)
			_, found := files[remotePath]
			if request.Method == http.MethodPost && remotePath == syncFormatPath && f.failNextMarkerCreates > 0 {
				f.failNextMarkerCreates--
				f.mu.Unlock()
				http.Error(writer, `{"message":"temporary marker write failure"}`, http.StatusServiceUnavailable)
				return
			}
			if request.Method == http.MethodPost && found {
				f.mu.Unlock()
				http.Error(writer, `{"message":"file already exists"}`, http.StatusUnprocessableEntity)
				return
			}
			if request.Method == http.MethodPut && (!found || body.SHA == "") {
				f.mu.Unlock()
				http.Error(writer, `{"messages":["sha is missing","sha is empty"]}`, http.StatusBadRequest)
				return
			}
			data, err := base64.StdEncoding.DecodeString(body.Content)
			if err != nil {
				f.mu.Unlock()
				http.Error(writer, `{"message":"bad content"}`, 400)
				return
			}
			files[remotePath] = data
			f.mu.Unlock()
			writeFakeJSON(writer, map[string]any{"content": map[string]any{"path": remotePath}})
			return
		}
		if request.Method == http.MethodGet {
			if data, found := files[remotePath]; found {
				key := repo + ":" + remotePath
				if f.failReadPaths[key] > 0 {
					f.failReadPaths[key]--
					f.mu.Unlock()
					http.Error(writer, `{"message":"temporary read failure"}`, http.StatusServiceUnavailable)
					return
				}
				if strings.HasPrefix(remotePath, "operations/") && path.Base(remotePath) != operationIndexName {
					f.operationReads++
				}
				f.mu.Unlock()
				writeFakeJSON(writer, map[string]any{"type": "file", "name": path.Base(remotePath), "path": remotePath, "sha": "test", "encoding": "base64", "content": base64.StdEncoding.EncodeToString(data), "size": len(data)})
				return
			}
			prefix := strings.TrimSuffix(remotePath, "/") + "/"
			child := map[string]bool{}
			for stored := range files {
				if strings.HasPrefix(stored, prefix) {
					remainder := strings.TrimPrefix(stored, prefix)
					childName := strings.Split(remainder, "/")[0]
					child[childName] = strings.Contains(remainder, "/")
				}
			}
			if len(child) == 0 {
				f.mu.Unlock()
				http.Error(writer, `{"message":"not found"}`, 404)
				return
			}
			items := make([]map[string]any, 0, len(child))
			for name, directory := range child {
				typeName := "file"
				childPath := prefix + name
				if directory {
					typeName = "dir"
				}
				items = append(items, map[string]any{"type": typeName, "name": name, "path": childPath})
			}
			f.mu.Unlock()
			writeFakeJSON(writer, items)
			return
		}
		f.mu.Unlock()
	}
	http.Error(writer, `{"message":"not found"}`, http.StatusNotFound)
}

func writeFakeJSON(writer http.ResponseWriter, value any) {
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(value)
}

// Keep time imported in this file, so an accidental conversion of retries to a
// busy loop is caught by Go's unused import check while maintaining compact tests.
var _ = time.Second
