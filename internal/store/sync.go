package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dingyu/offer-atlas/internal/domain"
	"github.com/google/uuid"
)

const (
	syncFormatPath  = ".offer-atlas-sync.json"
	mediaFormatPath = ".offer-atlas-media.json"
	// No .json suffix: previous application versions treat every JSON file in an
	// operation directory as a sync record. Keeping the discovery index outside
	// that set lets older devices continue to read new canonical records safely.
	operationIndexName              = "offer-atlas-operation-index"
	syncFormatVersion               = 1
	mediaSoftLimit            int64 = 400 * 1024 * 1024
	compatibilityCheckTimeout       = 6 * time.Second
)

var syncObjectSpecs = []syncObjectSpec{
	{Type: "company", Table: "companies", Columns: []string{"id", "name", "industry", "homepage", "notes", "created_at", "updated_at"}},
	{Type: "stage_type", Table: "stage_types", Columns: []string{"id", "name", "created_at", "updated_at"}},
	{Type: "campaign", Table: "campaigns", Columns: []string{"id", "company_id", "name", "opens_on", "closes_on", "source_url", "last_verified_on", "process_overview", "notes", "created_at", "updated_at"}},
	{Type: "position", Table: "positions", Columns: []string{"id", "campaign_id", "title", "job_code", "department", "location", "track", "source_url", "priority", "notes", "created_at", "updated_at"}},
	{Type: "resume", Table: "resumes", Columns: []string{"id", "name", "original_name", "stored_name", "mime_type", "size_bytes", "content_hash", "archived", "created_at", "updated_at"}, FileKind: "resume"},
	{Type: "application", Table: "applications", Columns: []string{"id", "position_id", "status", "submitted_on", "resume_id", "resume_name", "next_action", "next_action_on", "notes", "created_at", "updated_at", "channel"}},
	{Type: "stage", Table: "application_stages", Columns: []string{"id", "application_id", "sort_order", "name", "type", "status", "scheduled_start", "scheduled_end", "result_at", "source_url", "notes", "created_at", "updated_at"}},
	{Type: "resource_link", Table: "resource_links", Columns: []string{"id", "owner_type", "owner_id", "name", "url", "sort_order", "created_at", "updated_at"}},
	{Type: "position_attachment", Table: "position_attachments", Columns: []string{"id", "position_id", "original_name", "stored_name", "mime_type", "size_bytes", "created_at"}, FileKind: "position"},
	{Type: "resource_attachment", Table: "supplemental_attachments", Columns: []string{"id", "owner_type", "owner_id", "original_name", "stored_name", "mime_type", "size_bytes", "created_at"}, FileKind: "resource"},
	// Kept only for importing operation records produced by older versions.
	{Type: "application_resume", Table: "application_resumes", Columns: []string{"id", "application_id", "original_name", "stored_name", "mime_type", "size_bytes", "created_at"}, FileKind: "legacy_resume"},
}

// The first attempt starts immediately. Short retries make transient network
// failures recover quickly without leaving the interface in a vague waiting
// state for several minutes.
var syncRetryDelays = []time.Duration{0, 2 * time.Second, 5 * time.Second, 12 * time.Second}

type syncObjectSpec struct {
	Type                 string
	Table                string
	Columns              []string
	FileKind             string
	MinimumClientVersion string
	RequiredCapabilities []string
}

// syncRepositoryMarker lives in the primary repository and is deliberately
// separate from the data-format number. The latter identifies the on-disk
// record format; these fields decide whether a desktop build may safely read
// or write the current workspace at all.
type syncRepositoryMarker struct {
	Product              string   `json:"product"`
	Format               int      `json:"format"`
	Kind                 string   `json:"kind"`
	CreatedAt            string   `json:"created_at"`
	MinimumClientVersion string   `json:"minimum_client_version,omitempty"`
	RequiredCapabilities []string `json:"required_capabilities,omitempty"`
	CompatibilityEpoch   int      `json:"compatibility_epoch,omitempty"`
	UpdatedAt            string   `json:"updated_at,omitempty"`
}

type compatibilityCheckError struct{ err error }

func (e *compatibilityCheckError) Error() string {
	if e == nil || e.err == nil {
		return "无法确认云端兼容性"
	}
	return "确认云端兼容性: " + e.err.Error()
}

func (e *compatibilityCheckError) Unwrap() error { return e.err }

// compatibilityUnavailableError is intentionally non-retriable within the
// current batch: a malformed marker cannot become safe through another read.
type compatibilityUnavailableError struct{ message string }

func (e *compatibilityUnavailableError) Error() string { return e.message }

type compatibilityUpdateRequiredError struct {
	minimumVersion      string
	missingCapabilities []string
}

func (e *compatibilityUpdateRequiredError) Error() string {
	parts := make([]string, 0, 2)
	if e.minimumVersion != "" {
		parts = append(parts, "云端数据需要 Offer Atlas v"+e.minimumVersion+" 或更高版本")
	}
	if len(e.missingCapabilities) > 0 {
		parts = append(parts, "当前版本缺少所需同步能力："+strings.Join(e.missingCapabilities, "、"))
	}
	if len(parts) == 0 {
		return "云端数据需要更新后的 Offer Atlas 才能继续同步"
	}
	return strings.Join(parts, "；") + "。为保护数据，本次未传输任何记录。"
}

func syncSpec(objectType string) (syncObjectSpec, bool) {
	for _, spec := range syncObjectSpecs {
		if spec.Type == objectType {
			return spec, true
		}
	}
	return syncObjectSpec{}, false
}

type syncConfigRow struct {
	DeviceID           string
	DeviceName         string
	Owner              string
	PrimaryRepo        string
	Enabled            bool
	State              string
	LastSuccessAt      string
	LastCheckAt        string
	LastError          string
	NextSequence       int64
	InitialSyncPending bool
}

type syncObjectState struct {
	Type          string
	ID            string
	Version       int64
	RemoteVersion int64
	ContentHash   string
	Deleted       bool
	Dirty         bool
	UpdatedAt     string
}

type syncSnapshot struct {
	Type    string
	ID      string
	Payload json.RawMessage
	Hash    string
}

type syncOperation struct {
	ID            string          `json:"id"`
	DeviceID      string          `json:"deviceId"`
	Sequence      int64           `json:"sequence"`
	ObjectType    string          `json:"objectType"`
	ObjectID      string          `json:"objectId"`
	Action        string          `json:"action"`
	ObjectVersion int64           `json:"objectVersion"`
	BaseVersion   int64           `json:"baseVersion"`
	Payload       json.RawMessage `json:"payload"`
	CreatedAt     string          `json:"createdAt"`
}

type remoteOperationFile struct {
	file     giteeContent
	sequence int64
	knownSeq bool
}

type remoteOperationCandidate struct {
	repo     string
	deviceID string
	remoteOperationFile
}

// remoteSyncIntegrityError means the immutable remote log is incomplete or
// inconsistent. Retrying cannot recreate a missing operation file, so the
// worker surfaces it immediately instead of treating it as a network outage.
type remoteSyncIntegrityError struct{ message string }

func (e *remoteSyncIntegrityError) Error() string { return e.message }

type deferredRemoteOperation struct {
	operation syncOperation
	sequence  int64
	file      bool
}

type remoteOperationIndex struct {
	Product        string `json:"product"`
	Format         int    `json:"format"`
	DeviceID       string `json:"deviceId"`
	CanonicalFrom  int64  `json:"canonicalFrom"`
	LatestSequence int64  `json:"latestSequence"`
	UpdatedAt      string `json:"updatedAt"`
}

type cloudSync struct {
	store         *Store
	runMu         sync.Mutex
	activityMu    sync.RWMutex
	activity      syncActivity
	scheduleMu    sync.Mutex
	localTimer    *time.Timer
	periodicTimer *time.Timer
	stop          chan struct{}
	stopped       chan struct{}
	runCtx        context.Context
	cancel        context.CancelFunc
	workerWG      sync.WaitGroup
	stopOnce      sync.Once
	closed        bool
	importing     bool
	importMu      sync.Mutex
	lastActive    time.Time
	clientFactory func(token string) *giteeClient
}

// syncActivity is process-local on purpose: it describes the live sync batch,
// rather than a durable data state. The cutoff prevents edits made during a
// network run from being accidentally pulled into its upload.
type syncActivity struct {
	kind           string
	cutoffSequence int64
	progressDone   int
	progressTotal  int
	filesDone      int
	filesTotal     int
	retryAttempt   int
	retryMax       int
	retryError     string
	retryAfter     int
}

func (s *Store) CloudSyncStatus() (domain.CloudSyncStatus, error) {
	if s.cloud == nil {
		return domain.CloudSyncStatus{State: "local_only", Message: "仅在本机保存；可在此连接 Gitee 云同步"}, nil
	}
	return s.cloud.status()
}

func (s *Store) ConnectGitee(ctx context.Context, token string) (domain.GiteeConnectionPreview, error) {
	if s.cloud == nil {
		return domain.GiteeConnectionPreview{}, errors.New("同步服务尚未启动")
	}
	return s.cloud.connect(ctx, token)
}

// PendingGiteeConnectionPreview rebuilds the first-sync confirmation after an
// application restart. The token is read only from the local DPAPI store.
func (s *Store) PendingGiteeConnectionPreview(ctx context.Context) (domain.GiteeConnectionPreview, error) {
	if s.cloud == nil {
		return domain.GiteeConnectionPreview{}, errors.New("同步服务尚未启动")
	}
	return s.cloud.pendingConnectionPreview(ctx)
}

func (s *Store) ConfirmGiteeConnection(ctx context.Context, mode string) (domain.CloudSyncStatus, error) {
	if s.cloud == nil {
		return domain.CloudSyncStatus{}, errors.New("同步服务尚未启动")
	}
	return s.cloud.confirmConnection(ctx, mode)
}

func (s *Store) SyncGiteeNow(ctx context.Context) (domain.CloudSyncStatus, error) {
	if s.cloud == nil {
		return domain.CloudSyncStatus{}, errors.New("同步服务尚未启动")
	}
	err := s.cloud.run(ctx, true)
	status, statusErr := s.cloud.status()
	if err != nil {
		return status, err
	}
	return status, statusErr
}

// SyncBeforeClose waits for an in-flight cloud run and uploads only work that
// still has not reached Gitee. A remote update check with no local changes must
// not cause a second, redundant network round trip while the application exits.
func (s *Store) SyncBeforeClose(ctx context.Context) error {
	if s.cloud == nil {
		return nil
	}
	return s.cloud.syncBeforeClose(ctx)
}

func (s *Store) DisconnectGitee() error {
	if s.cloud == nil {
		return nil
	}
	return s.cloud.disconnect()
}

// DeleteGiteeSyncRepositories removes only the private repositories owned by
// Offer Atlas, creates a local recovery backup first, and then disconnects the
// local sync configuration. Business data and local attachments are preserved.
func (s *Store) DeleteGiteeSyncRepositories(ctx context.Context) ([]string, error) {
	if s.cloud == nil {
		return nil, errors.New("同步服务尚未启动")
	}
	return s.cloud.deleteRemoteRepositories(ctx)
}

func (s *Store) ListSyncConflicts() ([]domain.SyncConflict, error) {
	if s.cloud == nil {
		return []domain.SyncConflict{}, nil
	}
	return s.cloud.listConflicts()
}

func (s *Store) ResolveSyncConflict(ctx context.Context, id, choice string) error {
	if s.cloud == nil {
		return errors.New("同步服务尚未启动")
	}
	return s.cloud.resolveConflict(ctx, id, choice)
}

func (s *Store) NeedsSyncBeforeClose() bool {
	if s.cloud == nil {
		return false
	}
	config, err := s.cloud.config()
	if err != nil || !config.Enabled || config.InitialSyncPending {
		return false
	}
	// A newer client must take ownership of pending data that this build cannot
	// interpret. Keeping the current app open would not make that data safer.
	if config.State == "update_required" {
		return false
	}
	var pending int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sync_operations WHERE synced_at=''`).Scan(&pending); err != nil {
		return false
	}
	// A running check is allowed to finish before exit, but a prior failed check
	// with no unsynced local operation does not put local data at risk.
	return pending > 0 || config.State == "syncing"
}

func newCloudSync(store *Store) *cloudSync {
	runCtx, cancel := context.WithCancel(context.Background())
	return &cloudSync{store: store, stop: make(chan struct{}), stopped: make(chan struct{}), runCtx: runCtx, cancel: cancel, clientFactory: newGiteeClient}
}

func (c *cloudSync) client(token string) *giteeClient {
	if c.clientFactory != nil {
		return c.clientFactory(token)
	}
	return newGiteeClient(token)
}

func (c *cloudSync) start() {
	go func() {
		defer close(c.stopped)
		// Start the remote update check as soon as the local store is ready. The
		// actual work remains asynchronous, so application startup is never held.
		c.checkOnStartup()
		<-c.stop
	}()
}

func (c *cloudSync) close() {
	if c == nil {
		return
	}
	c.stopOnce.Do(func() {
		c.scheduleMu.Lock()
		c.closed = true
		if c.localTimer != nil {
			c.localTimer.Stop()
			c.localTimer = nil
		}
		if c.periodicTimer != nil {
			c.periodicTimer.Stop()
			c.periodicTimer = nil
		}
		c.scheduleMu.Unlock()
		c.cancel()
		close(c.stop)
	})
	select {
	case <-c.stopped:
	case <-time.After(2 * time.Second):
		// Startup status reads are local and normally finish immediately; keep
		// shutdown bounded if SQLite is already being torn down.
	}
	done := make(chan struct{})
	go func() {
		c.workerWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		// HTTP requests use runCtx and should normally stop promptly. Keep a
		// bounded shutdown in case a third-party transport misbehaves.
	}
}

func (c *cloudSync) checkOnStartup() {
	config, err := c.config()
	if err != nil || !config.Enabled {
		return
	}
	if config.InitialSyncPending {
		// A first-sync decision is intentionally durable. Do not turn it into a
		// background check, because the scheduler will correctly refuse to run until
		// the user confirms upload, download, or merge.
		if config.State != "pending_confirmation" {
			c.setState("pending_confirmation", "首次同步等待确认")
		}
		return
	}
	// Surface the check immediately instead of leaving a stale "synced" label
	// visible while the first background request is being scheduled.
	c.setState("syncing", "正在检查云端是否有更新")
	c.runAsync()
}

func (c *cloudSync) afterLocalMutation() {
	if c == nil || c.isImporting() {
		return
	}
	config, err := c.config()
	if err != nil || !config.Enabled || config.InitialSyncPending {
		return
	}
	if err := c.captureLocalChanges(); err != nil {
		c.setSyncFailure(err)
		return
	}
	if err := c.markPendingAfterLocalMutation(); err != nil {
		c.setSyncFailure(err)
		return
	}
	// The first local edit opens a fixed sync window. Later edits before the
	// timer fires join this batch instead of indefinitely postponing it.
	c.scheduleLocalSync(10 * time.Second)
}

// markPendingAfterLocalMutation makes the local-first synchronization contract
// visible immediately. captureLocalChanges has already committed the immutable
// operations; the timer only decides when their next cloud upload begins.
func (c *cloudSync) markPendingAfterLocalMutation() error {
	var pending, conflicts int
	if err := c.store.db.QueryRow(`SELECT COUNT(*) FROM sync_operations WHERE synced_at=''`).Scan(&pending); err != nil {
		return err
	}
	if err := c.store.db.QueryRow(`SELECT COUNT(*) FROM sync_conflicts WHERE status='open'`).Scan(&conflicts); err != nil {
		return err
	}
	if pending == 0 || conflicts > 0 {
		return nil
	}
	if c.activitySnapshot().kind != "" {
		// A running batch owns the visible state. These new immutable operations
		// remain unsynced and are exposed as the next batch in status().
		return nil
	}
	_, err := c.store.db.Exec(`UPDATE sync_config SET state='pending', last_error='', updated_at=? WHERE id=1`, nowString())
	return err
}

func (c *cloudSync) scheduleLocalSync(delay time.Duration) {
	if c == nil {
		return
	}
	config, err := c.config()
	if err != nil || !config.Enabled || config.InitialSyncPending {
		return
	}
	if c.activitySnapshot().kind != "" {
		// The active run will schedule this debounce window from its successful
		// completion, so an edit cannot start a competing timer mid-run.
		return
	}
	c.scheduleMu.Lock()
	if c.localTimer != nil {
		if delay > 0 {
			// Normal local edits belong to the window opened by the first edit.
			// Do not turn ordinary editing into an unbounded tail debounce.
			c.scheduleMu.Unlock()
			return
		}
		// A zero-delay caller explicitly requests an immediate run, for example
		// after resolving a conflict. It is allowed to replace a pending window.
		c.localTimer.Stop()
	}
	if c.periodicTimer != nil {
		c.periodicTimer.Stop()
		c.periodicTimer = nil
	}
	c.localTimer = time.AfterFunc(delay, func() {
		c.scheduleMu.Lock()
		c.localTimer = nil
		c.scheduleMu.Unlock()
		c.runAsync()
	})
	c.scheduleMu.Unlock()
}

func (c *cloudSync) schedulePeriodicCheck(delay time.Duration) {
	if c == nil {
		return
	}
	config, err := c.config()
	if err != nil || !config.Enabled || config.InitialSyncPending {
		return
	}
	c.scheduleMu.Lock()
	if c.periodicTimer != nil {
		c.periodicTimer.Stop()
	}
	c.periodicTimer = time.AfterFunc(delay, func() {
		c.scheduleMu.Lock()
		c.periodicTimer = nil
		localScheduled := c.localTimer != nil
		c.scheduleMu.Unlock()
		if localScheduled {
			// The local debounce owns the next run. Its successful completion
			// starts a fresh 30-second cloud-check window.
			return
		}
		c.runAsync()
	})
	c.scheduleMu.Unlock()
}

func (c *cloudSync) cancelLocalTimer() {
	c.scheduleMu.Lock()
	if c.localTimer != nil {
		c.localTimer.Stop()
		c.localTimer = nil
	}
	c.scheduleMu.Unlock()
}

func (c *cloudSync) scheduleAfterRun(success bool) {
	if !success {
		// runLocked already performs the configured short retries.
		// The recurring 30-second clock starts only after a successful remote
		// check, so an actionable failure remains visible instead of looping.
		return
	}
	var pending, conflicts int
	if err := c.store.db.QueryRow(`SELECT COUNT(*) FROM sync_operations WHERE synced_at=''`).Scan(&pending); err != nil {
		return
	}
	if err := c.store.db.QueryRow(`SELECT COUNT(*) FROM sync_conflicts WHERE status='open'`).Scan(&conflicts); err != nil {
		return
	}
	if conflicts > 0 {
		// The per-device log cannot skip a blocked sequence. Wait for conflict
		// resolution before starting another automatic upload attempt.
		return
	}
	if pending > 0 {
		// Edits made while the just-finished run was active are a new batch.
		// Their 10-second window starts only after that run succeeds.
		c.scheduleLocalSync(10 * time.Second)
		return
	}
	c.schedulePeriodicCheck(30 * time.Second)
}

func (c *cloudSync) runAsync() {
	go func() {
		_ = c.run(c.runCtx, false)
	}()
}

func (c *cloudSync) run(ctx context.Context, manual bool) error {
	if c == nil {
		return errors.New("cloud sync is unavailable")
	}
	c.scheduleMu.Lock()
	if c.closed {
		c.scheduleMu.Unlock()
		return context.Canceled
	}
	c.workerWG.Add(1)
	c.scheduleMu.Unlock()
	defer c.workerWG.Done()
	c.runMu.Lock()
	defer c.runMu.Unlock()
	return c.runLocked(ctx, manual)
}

func (c *cloudSync) syncBeforeClose(ctx context.Context) error {
	if c == nil {
		return nil
	}
	// Waiting for this lock lets an already-running check or upload settle
	// first. This keeps shutdown serial without starting an unnecessary second
	// check when the only work was pulling remote updates.
	c.runMu.Lock()
	defer c.runMu.Unlock()

	config, err := c.config()
	if err != nil {
		return err
	}
	if !config.Enabled || config.InitialSyncPending {
		return nil
	}
	if config.State == "failed" {
		if config.LastError != "" {
			return errors.New(config.LastError)
		}
		return errors.New("上一次云同步未完成，请检查网络或 Gitee 连接")
	}
	if config.State == "update_required" {
		return nil
	}
	if err := c.captureLocalChanges(); err != nil {
		c.setSyncFailure(err)
		return err
	}
	var pending int
	if err := c.store.db.QueryRow(`SELECT COUNT(*) FROM sync_operations WHERE synced_at=''`).Scan(&pending); err != nil {
		return err
	}
	if pending == 0 {
		return nil
	}
	return c.runLocked(ctx, true)
}

func (c *cloudSync) runLocked(ctx context.Context, manual bool) error {
	c.cancelLocalTimer()
	config, err := c.config()
	if err != nil {
		return err
	}
	if !config.Enabled || config.InitialSyncPending {
		return nil
	}
	if err := c.captureLocalChanges(); err != nil {
		c.setSyncFailure(err)
		return err
	}
	cutoffSequence, err := c.pendingSequenceCutoff()
	if err != nil {
		return err
	}
	c.setActivity("compatibility", cutoffSequence)
	defer c.clearActivity()
	c.setState("syncing", "正在确认云端兼容性")
	var lastErr error
	for attempt, delay := range syncRetryDelays {
		if attempt > 0 {
			c.setActivityRetry(attempt, len(syncRetryDelays), lastErr, delay)
			if err := waitSyncDelay(ctx, delay); err != nil {
				return err
			}
			c.clearActivityRetry()
		}
		lastErr = c.syncOnce(ctx, config, cutoffSequence)
		if lastErr == nil {
			status, _ := c.status()
			if status.ConflictCount > 0 {
				c.setState("conflict", "存在需要处理的同步冲突")
			} else if status.PendingChanges > 0 {
				c.setState("pending", "本地修改将在下一批同步")
			} else {
				c.setState("synced", "已与 Gitee 云端同步")
			}
			c.clearActivity()
			c.scheduleAfterRun(true)
			return nil
		}
		var updateRequired *compatibilityUpdateRequiredError
		if errors.As(lastErr, &updateRequired) {
			c.setState("update_required", updateRequired.Error())
			c.clearActivity()
			return lastErr
		}
		var unavailable *compatibilityUnavailableError
		if errors.As(lastErr, &unavailable) {
			c.setState("compatibility_unavailable", unavailable.Error())
			c.clearActivity()
			return lastErr
		}
		var gerr *giteeError
		if errors.As(lastErr, &gerr) && gerr.actionable() {
			break
		}
		var integrityErr *remoteSyncIntegrityError
		if errors.As(lastErr, &integrityErr) {
			break
		}
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return ctx.Err()
	}
	var compatibilityErr *compatibilityCheckError
	if errors.As(lastErr, &compatibilityErr) {
		c.setState("compatibility_unavailable", "暂时无法确认云端兼容性。请检查网络后重新尝试；本次未传输任何记录。")
		c.clearActivity()
		c.schedulePeriodicCheck(10 * time.Minute)
		return lastErr
	}
	c.setSyncFailure(lastErr)
	c.clearActivity()
	c.scheduleAfterRun(false)
	return lastErr
}

func waitSyncDelay(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func safeSyncError(err error) string {
	if err == nil {
		return "未知错误"
	}
	message := err.Error()
	message = strings.ReplaceAll(message, "access_token", "token")
	// Keep enough context for a remote path and the actionable Gitee response.
	// The UI wraps this text; truncating at 180 characters hid the exact file
	// that prevented recovery.
	if len(message) > 1000 {
		message = message[:1000] + "…"
	}
	return message
}

func (c *cloudSync) setSyncFailure(err error) {
	c.setState("failed", safeSyncError(err))
}

func (c *cloudSync) syncOnce(ctx context.Context, config syncConfigRow, cutoffSequence int64) error {
	token, err := c.readToken()
	if err != nil {
		return err
	}
	client := c.client(token)
	c.setActivity("compatibility", cutoffSequence)
	if _, _, err := c.checkRemoteCompatibility(ctx, client, config); err != nil {
		return err
	}
	if err := c.pullRemote(ctx, client, config); err != nil {
		return err
	}
	if err := c.raiseRemoteCompatibilityForPending(ctx, client, config, cutoffSequence); err != nil {
		return err
	}
	if err := c.uploadPending(ctx, client, config, cutoffSequence); err != nil {
		return err
	}
	_, err = c.store.db.Exec(`UPDATE sync_config SET last_check_at=?, last_success_at=?, last_error='', updated_at=? WHERE id=1`, nowString(), nowString(), nowString())
	return err
}

func (c *cloudSync) pendingSequenceCutoff() (int64, error) {
	var cutoff sql.NullInt64
	if err := c.store.db.QueryRow(`SELECT MAX(sequence) FROM sync_operations WHERE synced_at=''`).Scan(&cutoff); err != nil {
		return 0, err
	}
	return cutoff.Int64, nil
}

func (c *cloudSync) setActivity(kind string, cutoffSequence int64) {
	c.activityMu.Lock()
	c.activity = syncActivity{kind: kind, cutoffSequence: cutoffSequence, retryMax: 4}
	c.activityMu.Unlock()
}

func (c *cloudSync) setActivityProgress(kind string, total, filesTotal int) {
	c.activityMu.Lock()
	c.activity.kind = kind
	c.activity.progressDone = 0
	c.activity.progressTotal = total
	c.activity.filesDone = 0
	c.activity.filesTotal = filesTotal
	c.activity.retryAttempt = 0
	c.activity.retryError = ""
	c.activity.retryAfter = 0
	c.activity.retryMax = 4
	c.activityMu.Unlock()
}

func (c *cloudSync) advanceActivityProgress(file bool) {
	c.activityMu.Lock()
	c.activity.progressDone++
	if file {
		c.activity.filesDone++
	}
	c.activityMu.Unlock()
}

func (c *cloudSync) addActivityFileTotal() {
	c.activityMu.Lock()
	c.activity.filesTotal++
	c.activityMu.Unlock()
}

func (c *cloudSync) setActivityRetry(attempt, max int, err error, delay time.Duration) {
	c.activityMu.Lock()
	c.activity.retryAttempt = attempt
	c.activity.retryMax = max
	c.activity.retryError = safeSyncError(err)
	c.activity.retryAfter = int(delay.Round(time.Second) / time.Second)
	c.activityMu.Unlock()
}

func (c *cloudSync) clearActivityRetry() {
	c.activityMu.Lock()
	c.activity.retryAttempt = 0
	c.activity.retryError = ""
	c.activity.retryAfter = 0
	c.activityMu.Unlock()
}

func (c *cloudSync) clearActivity() {
	c.setActivity("", 0)
}

func (c *cloudSync) activitySnapshot() syncActivity {
	c.activityMu.RLock()
	defer c.activityMu.RUnlock()
	return c.activity
}

func (c *cloudSync) config() (syncConfigRow, error) {
	var row syncConfigRow
	var enabled, initial int
	err := c.store.db.QueryRow(`SELECT device_id, device_name, owner, primary_repo, enabled, state, last_success_at, last_check_at, last_error, next_sequence, initial_sync_pending FROM sync_config WHERE id=1`).Scan(
		&row.DeviceID, &row.DeviceName, &row.Owner, &row.PrimaryRepo, &enabled, &row.State, &row.LastSuccessAt, &row.LastCheckAt, &row.LastError, &row.NextSequence, &initial,
	)
	if err != nil {
		return syncConfigRow{}, err
	}
	row.Enabled = enabled != 0
	row.InitialSyncPending = initial != 0
	return row, nil
}

func (c *cloudSync) setState(state, message string) {
	if c == nil {
		return
	}
	_, _ = c.store.db.Exec(`UPDATE sync_config SET state=?, last_error=?, last_check_at=?, updated_at=? WHERE id=1`, state, messageIfFailure(state, message), nowString(), nowString())
}

func messageIfFailure(state, message string) string {
	if state == "failed" || state == "conflict" || state == "update_required" || state == "compatibility_unavailable" {
		return message
	}
	return ""
}

func (c *cloudSync) status() (domain.CloudSyncStatus, error) {
	config, err := c.config()
	if err != nil {
		return domain.CloudSyncStatus{}, err
	}
	status := domain.CloudSyncStatus{State: config.State, Owner: config.Owner, PrimaryRepo: config.PrimaryRepo, DeviceName: config.DeviceName, LastSuccessAt: config.LastSuccessAt, LastCheckedAt: config.LastCheckAt}
	if !config.Enabled {
		status.State, status.Message = "local_only", "仅在本机保存；可在此连接 Gitee 云同步"
		return status, nil
	}
	if err := c.store.db.QueryRow(`SELECT COUNT(*) FROM sync_operations WHERE synced_at=''`).Scan(&status.PendingChanges); err != nil {
		return domain.CloudSyncStatus{}, err
	}
	if err := c.store.db.QueryRow(`SELECT COUNT(*) FROM sync_conflicts WHERE status='open'`).Scan(&status.ConflictCount); err != nil {
		return domain.CloudSyncStatus{}, err
	}
	if marker, _, cacheErr := c.cachedRemoteCompatibility(); cacheErr == nil {
		status.MinimumClientVersion = marker.MinimumClientVersion
		status.RequiredCapabilities = marker.RequiredCapabilities
	}
	activity := c.activitySnapshot()
	status.Activity = activity.kind
	status.ProgressDone = activity.progressDone
	status.ProgressTotal = activity.progressTotal
	status.FilesDone = activity.filesDone
	status.FilesTotal = activity.filesTotal
	status.RetryAttempt = activity.retryAttempt
	status.RetryMax = activity.retryMax
	status.RetryError = activity.retryError
	status.RetryAfter = activity.retryAfter
	if activity.kind == "syncing" || activity.kind == "uploading" || activity.kind == "compatibility" {
		if err := c.store.db.QueryRow(`SELECT COUNT(*) FROM sync_operations WHERE synced_at='' AND sequence<=?`, activity.cutoffSequence).Scan(&status.ActiveChanges); err != nil {
			return domain.CloudSyncStatus{}, err
		}
		status.QueuedChanges = status.PendingChanges - status.ActiveChanges
	} else if activity.kind == "checking" || activity.kind == "downloading" {
		status.QueuedChanges = status.PendingChanges
	}
	// A status request may arrive in the few milliseconds before the sync worker
	// records its in-memory batch. Keep the label truthful during that window.
	if status.Activity == "" && config.State == "syncing" {
		if status.PendingChanges > 0 {
			status.Activity, status.ActiveChanges = "uploading", status.PendingChanges
		} else {
			status.Activity = "checking"
		}
	}
	status.CanSync = !config.InitialSyncPending && config.State != "update_required"
	switch config.State {
	case "synced":
		status.Message = "已与 Gitee 云端同步"
	case "syncing":
		if status.RetryAttempt > 0 && status.RetryAfter > 0 {
			status.Message = fmt.Sprintf("同步请求暂时失败，将在 %d 秒后重试（%d/%d）", status.RetryAfter, status.RetryAttempt, status.RetryMax-1)
		} else if status.Activity == "compatibility" {
			if status.QueuedChanges > 0 {
				status.Message = fmt.Sprintf("正在确认云端兼容性；本机有 %d 条修改等待同步", status.QueuedChanges)
			} else {
				status.Message = "正在确认云端兼容性"
			}
		} else if status.Activity == "downloading" {
			if status.ProgressTotal > 0 {
				status.Message = fmt.Sprintf("正在恢复云端数据（%d/%d）", status.ProgressDone, status.ProgressTotal)
			} else {
				status.Message = "正在恢复云端数据"
			}
		} else if status.Activity == "checking" {
			if status.QueuedChanges > 0 {
				status.Message = fmt.Sprintf("正在检查云端更新；本机有 %d 条修改将在下一批同步", status.QueuedChanges)
			} else {
				status.Message = "正在检查云端是否有更新"
			}
		} else {
			if status.ProgressTotal > 0 {
				status.Message = fmt.Sprintf("正在上传本机更改（%d/%d）", status.ProgressDone, status.ProgressTotal)
			} else if status.QueuedChanges > 0 {
				status.Message = fmt.Sprintf("正在同步本机更改；另有 %d 条修改将在下一批同步", status.QueuedChanges)
			} else {
				status.Message = "正在同步本机更改"
			}
		}
	case "pending":
		status.Message = "本地修改已保存，将在约 10 秒后同步"
	case "conflict":
		status.Message = "需要处理冲突；本地和云端数据都已保留"
	case "failed":
		status.Message = "本地数据已保存，云同步未完成"
		if config.LastError != "" {
			detail := strings.TrimPrefix(config.LastError, "本地数据已保存，云同步未完成：")
			status.Message += "：" + detail
		}
	case "update_required":
		status.Message = config.LastError
		if status.Message == "" {
			status.Message = "云端数据需要更新后的 Offer Atlas 才能继续同步"
		}
	case "compatibility_unavailable":
		status.Message = config.LastError
		if status.Message == "" {
			status.Message = "暂时无法确认云端兼容性；本次未传输任何记录"
		}
	default:
		status.Message = "Gitee 云同步等待确认"
	}
	return status, nil
}

func (c *cloudSync) tokenPath() string {
	return filepath.Join(filepath.Dir(c.store.path), "gitee-token.dpapi")
}

func (c *cloudSync) saveToken(token string) error {
	protected, err := protectLocalSecret([]byte(strings.TrimSpace(token)))
	if err != nil {
		return err
	}
	return writeSyncedFile(c.tokenPath(), []byte(base64.StdEncoding.EncodeToString(protected)))
}

func (c *cloudSync) readToken() (string, error) {
	encoded, err := os.ReadFile(c.tokenPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", errors.New("Gitee 令牌不存在，请重新连接")
		}
		return "", err
	}
	protected, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil {
		return "", errors.New("Gitee 令牌存储无效，请重新连接")
	}
	plain, err := unprotectLocalSecret(protected)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func (c *cloudSync) clearToken() error {
	err := os.Remove(c.tokenPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (c *cloudSync) isImporting() bool {
	c.importMu.Lock()
	defer c.importMu.Unlock()
	return c.importing
}

func (c *cloudSync) withImporting(run func() error) error {
	c.importMu.Lock()
	c.importing = true
	c.importMu.Unlock()
	defer func() {
		c.importMu.Lock()
		c.importing = false
		c.importMu.Unlock()
	}()
	return run()
}

// connect validates a personal token, finds a marked Offer Atlas repository
// or creates a dedicated private one, then returns a summary for confirmation.
func (c *cloudSync) connect(ctx context.Context, token string) (domain.GiteeConnectionPreview, error) {
	if runtime.GOOS != "windows" {
		return domain.GiteeConnectionPreview{}, errors.New("Gitee 云同步 V1 仅支持 Windows")
	}
	if strings.TrimSpace(token) == "" {
		return domain.GiteeConnectionPreview{}, errors.New("请输入 Gitee 私人令牌")
	}
	client := c.client(token)
	user, err := client.currentUser(ctx)
	if err != nil {
		return domain.GiteeConnectionPreview{}, fmt.Errorf("验证 Gitee 令牌: %w", err)
	}
	repo, err := c.findOrCreatePrimaryRepo(ctx, client, user.Login)
	if err != nil {
		return domain.GiteeConnectionPreview{}, err
	}
	config, err := c.config()
	if err != nil {
		return domain.GiteeConnectionPreview{}, err
	}
	// Do not read operation history or persist a connection that this desktop
	// build cannot safely use. A newly created primary marker has no minimum
	// requirement, while an existing workspace may intentionally require a
	// newer release.
	config.Owner, config.PrimaryRepo = user.Login, repo.Name
	if _, _, err := c.checkRemoteCompatibility(ctx, client, config); err != nil {
		return domain.GiteeConnectionPreview{}, err
	}
	if err := c.saveToken(token); err != nil {
		return domain.GiteeConnectionPreview{}, err
	}
	if config.DeviceID == "" {
		config.DeviceID = uuid.NewString()
	}
	if config.DeviceName == "" {
		config.DeviceName = defaultDeviceName()
	}
	if _, err := c.store.db.Exec(`UPDATE sync_config SET device_id=?, device_name=?, owner=?, primary_repo=?, enabled=1, state='pending_confirmation', initial_sync_pending=1, last_error='', updated_at=? WHERE id=1`, config.DeviceID, config.DeviceName, user.Login, repo.Name, nowString()); err != nil {
		return domain.GiteeConnectionPreview{}, err
	}
	local, err := c.localSummary()
	if err != nil {
		return domain.GiteeConnectionPreview{}, err
	}
	cloud, err := c.remoteSummary(ctx, client, user.Login, repo.Name)
	if err != nil {
		return domain.GiteeConnectionPreview{}, err
	}
	recommended := "merge"
	if summaryEmpty(cloud) {
		recommended = "upload"
	} else if summaryEmpty(local) {
		recommended = "download"
	}
	return domain.GiteeConnectionPreview{Account: user.Login, PrimaryRepo: repo.Name, Local: local, Cloud: cloud, Recommended: recommended, NeedsConfirm: true}, nil
}

func (c *cloudSync) pendingConnectionPreview(ctx context.Context) (domain.GiteeConnectionPreview, error) {
	config, err := c.config()
	if err != nil {
		return domain.GiteeConnectionPreview{}, err
	}
	if !config.Enabled || !config.InitialSyncPending || config.Owner == "" || config.PrimaryRepo == "" {
		return domain.GiteeConnectionPreview{}, errors.New("没有等待确认的首次 Gitee 同步")
	}
	token, err := c.readToken()
	if err != nil {
		return domain.GiteeConnectionPreview{}, err
	}
	client := c.client(token)
	user, err := client.currentUser(ctx)
	if err != nil {
		return domain.GiteeConnectionPreview{}, fmt.Errorf("读取 Gitee 账号: %w", err)
	}
	if user.Login != config.Owner {
		return domain.GiteeConnectionPreview{}, errors.New("当前令牌对应的 Gitee 账号与待确认账号不一致")
	}
	if _, _, err := c.checkRemoteCompatibility(ctx, client, config); err != nil {
		return domain.GiteeConnectionPreview{}, err
	}
	local, err := c.localSummary()
	if err != nil {
		return domain.GiteeConnectionPreview{}, err
	}
	cloud, err := c.remoteSummary(ctx, client, config.Owner, config.PrimaryRepo)
	if err != nil {
		return domain.GiteeConnectionPreview{}, err
	}
	recommended := "merge"
	if summaryEmpty(cloud) {
		recommended = "upload"
	} else if summaryEmpty(local) {
		recommended = "download"
	}
	return domain.GiteeConnectionPreview{Account: config.Owner, PrimaryRepo: config.PrimaryRepo, Local: local, Cloud: cloud, Recommended: recommended, NeedsConfirm: true}, nil
}

func defaultDeviceName() string {
	name, err := os.Hostname()
	if err != nil || strings.TrimSpace(name) == "" {
		return "Windows 设备"
	}
	return name
}

func summaryEmpty(value domain.SyncDataSummary) bool {
	return value.Companies == 0 && value.Campaigns == 0 && value.Positions == 0 && value.Applications == 0 && value.Stages == 0 && value.Attachments == 0 && value.Resumes == 0
}

func (c *cloudSync) confirmConnection(ctx context.Context, mode string) (domain.CloudSyncStatus, error) {
	// First-sync confirmation changes durable state before it begins the remote
	// transfer. Keep that transition serial, and make a duplicate UI/API call
	// harmless once the first caller has already started it.
	c.runMu.Lock()
	defer c.runMu.Unlock()

	config, err := c.config()
	if err != nil {
		return domain.CloudSyncStatus{}, err
	}
	if !config.Enabled {
		return domain.CloudSyncStatus{}, errors.New("没有待确认的 Gitee 连接")
	}
	if mode != "upload" && mode != "download" && mode != "merge" {
		return domain.CloudSyncStatus{}, errors.New("无效的首次同步方式")
	}
	if !config.InitialSyncPending {
		return c.status()
	}
	token, err := c.readToken()
	if err != nil {
		return domain.CloudSyncStatus{}, err
	}
	client := c.client(token)
	if _, _, err := c.checkRemoteCompatibility(ctx, client, config); err != nil {
		var updateRequired *compatibilityUpdateRequiredError
		if errors.As(err, &updateRequired) {
			c.setState("update_required", updateRequired.Error())
		} else {
			var unavailable *compatibilityUnavailableError
			if errors.As(err, &unavailable) {
				c.setState("compatibility_unavailable", unavailable.Error())
			}
		}
		status, _ := c.status()
		return status, err
	}
	// A user may have removed the dedicated Offer Atlas repositories after a
	// broken remote log.  When the confirmation explicitly selects upload and
	// the newly connected primary repository is empty, the old local tracking
	// history must not make the upload look complete.  Reset only synchronization
	// metadata; business tables and local attachment files remain untouched.
	if mode == "upload" {
		cloud, summaryErr := c.remoteSummary(ctx, client, config.Owner, config.PrimaryRepo)
		if summaryErr != nil {
			return domain.CloudSyncStatus{}, fmt.Errorf("确认云端为空以重新上传: %w", summaryErr)
		}
		hasHistory, historyErr := c.remoteHasSyncHistory(ctx, client, config.Owner, config.PrimaryRepo)
		if historyErr != nil {
			return domain.CloudSyncStatus{}, fmt.Errorf("检查云端同步历史: %w", historyErr)
		}
		if summaryEmpty(cloud) && !hasHistory {
			if err := c.resetTrackingForFreshRemote(); err != nil {
				return domain.CloudSyncStatus{}, err
			}
		}
	}
	if mode == "download" || mode == "merge" {
		if _, err := c.store.CreateBackup(); err != nil {
			return domain.CloudSyncStatus{}, fmt.Errorf("首次导入前创建本地完整备份: %w", err)
		}
	}
	if err := c.initializeTracking(); err != nil {
		return domain.CloudSyncStatus{}, err
	}
	if _, err := c.store.db.Exec(`UPDATE sync_config SET initial_sync_pending=0, state='pending', updated_at=? WHERE id=1`, nowString()); err != nil {
		return domain.CloudSyncStatus{}, err
	}
	// Download/merge first consumes the immutable remote log. Upload will then
	// send only still-dirty local objects; it never overwrites remote data.
	if err := c.runLocked(ctx, true); err != nil {
		return c.status()
	}
	return c.status()
}

// resetTrackingForFreshRemote prepares a clean local sync ledger after the
// user has deliberately replaced the dedicated remote repositories.  It does
// not touch business data, attachments, the device ID, or the protected token.
// The next captureLocalChanges call therefore creates a complete upload batch.
func (c *cloudSync) resetTrackingForFreshRemote() error {
	tx, err := c.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, table := range []string{
		"sync_operations",
		"sync_object_states",
		"sync_applied_operations",
		"sync_conflicts",
		"sync_media_files",
		"sync_remote_cursors",
		"sync_repo_provisioning",
	} {
		if _, err := tx.Exec(`DELETE FROM ` + table); err != nil {
			return fmt.Errorf("清理本地同步追踪数据: %w", err)
		}
	}
	if _, err := tx.Exec(`UPDATE sync_config SET next_sequence=1, last_success_at='', last_check_at='', last_error='', state='pending', updated_at=? WHERE id=1`, nowString()); err != nil {
		return fmt.Errorf("重置本地同步状态: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("保存本地同步重置状态: %w", err)
	}
	return nil
}

func (c *cloudSync) disconnect() error {
	if err := c.clearToken(); err != nil {
		return err
	}
	_, err := c.store.db.Exec(`UPDATE sync_config SET owner='', primary_repo='', enabled=0, state='local_only', initial_sync_pending=0, last_error='', updated_at=? WHERE id=1`, nowString())
	return err
}

func (c *cloudSync) deleteRemoteRepositories(ctx context.Context) ([]string, error) {
	c.runMu.Lock()
	defer c.runMu.Unlock()

	config, err := c.config()
	if err != nil {
		return nil, err
	}
	if !config.Enabled || strings.TrimSpace(config.Owner) == "" {
		return nil, errors.New("Gitee 云同步尚未连接")
	}
	token, err := c.readToken()
	if err != nil {
		return nil, err
	}
	client := c.client(token)
	user, err := client.currentUser(ctx)
	if err != nil {
		return nil, fmt.Errorf("验证 Gitee 账号: %w", err)
	}
	if !strings.EqualFold(user.Login, config.Owner) {
		return nil, errors.New("当前令牌对应的 Gitee 账号与同步账号不一致")
	}
	repos, err := client.listRepos(ctx)
	if err != nil {
		return nil, fmt.Errorf("读取 Gitee 仓库列表: %w", err)
	}
	candidates := make([]giteeRepo, 0)
	for _, repo := range repos {
		if !repo.Private {
			continue
		}
		if isOfferAtlasPrimaryName(repo.Name) && c.repoHasMarker(ctx, client, config.Owner, repo.Name, syncFormatPath, "primary") {
			candidates = append(candidates, repo)
			continue
		}
		if isOfferAtlasMediaName(repo.Name) && c.repoHasMarker(ctx, client, config.Owner, repo.Name, mediaFormatPath, "media") {
			candidates = append(candidates, repo)
		}
	}
	if len(candidates) == 0 {
		return nil, errors.New("没有找到带有 Offer Atlas 标识的专用云端仓库；为保护数据，未执行删除")
	}
	if _, err := c.store.CreateBackup(); err != nil {
		return nil, fmt.Errorf("删除云端仓库前创建本地完整备份: %w", err)
	}
	// Delete media repositories first so the primary metadata index never points
	// at a repository that is still being removed.
	sort.SliceStable(candidates, func(i, j int) bool {
		iMedia := isOfferAtlasMediaName(candidates[i].Name)
		jMedia := isOfferAtlasMediaName(candidates[j].Name)
		if iMedia != jMedia {
			return iMedia
		}
		return strings.ToLower(candidates[i].Name) < strings.ToLower(candidates[j].Name)
	})
	c.setActivityProgress("deleting", len(candidates), 0)
	c.setState("syncing", "正在删除 Gitee 云端仓库")
	defer c.clearActivity()
	deleted := make([]string, 0, len(candidates))
	failed := make([]string, 0)
	for _, repo := range candidates {
		if err := client.deleteRepo(ctx, config.Owner, repo.Name); err != nil {
			failed = append(failed, fmt.Sprintf("%s：%v", repo.Name, err))
			continue
		}
		deleted = append(deleted, repo.Name)
		c.advanceActivityProgress(false)
	}
	if len(failed) > 0 {
		// Stop background sync after a partial destructive action. The user can
		// reconnect later to retry the remaining repositories without risking
		// writes to a partially rebuilt workspace.
		_ = c.disconnect()
		return deleted, fmt.Errorf("部分云端仓库删除失败：%s；已删除 %d 个专用仓库，本地数据仍保留", strings.Join(failed, "；"), len(deleted))
	}
	if err := c.disconnect(); err != nil {
		return deleted, fmt.Errorf("云端仓库已删除，但断开本机同步配置失败：%w", err)
	}
	return deleted, nil
}

func (c *cloudSync) localSummary() (domain.SyncDataSummary, error) {
	var result domain.SyncDataSummary
	queries := []struct {
		destination *int
		table       string
	}{
		{&result.Companies, "companies"}, {&result.Campaigns, "campaigns"}, {&result.Positions, "positions"},
		{&result.Applications, "applications"}, {&result.Stages, "application_stages"}, {&result.Resumes, "resumes"},
	}
	for _, item := range queries {
		if err := c.store.db.QueryRow(`SELECT COUNT(*) FROM ` + item.table).Scan(item.destination); err != nil {
			return domain.SyncDataSummary{}, err
		}
	}
	if err := c.store.db.QueryRow(`SELECT (SELECT COUNT(*) FROM position_attachments) + (SELECT COUNT(*) FROM supplemental_attachments)`).Scan(&result.Attachments); err != nil {
		return domain.SyncDataSummary{}, err
	}
	return result, nil
}

func (c *cloudSync) findOrCreatePrimaryRepo(ctx context.Context, client *giteeClient, owner string) (giteeRepo, error) {
	repos, err := client.listRepos(ctx)
	if err != nil {
		return giteeRepo{}, err
	}
	byName := make(map[string]giteeRepo, len(repos))
	for _, repo := range repos {
		byName[strings.ToLower(repo.Name)] = repo
		if !isOfferAtlasPrimaryName(repo.Name) || !repo.Private {
			continue
		}
		if c.repoHasMarker(ctx, client, owner, repo.Name, syncFormatPath, "primary") {
			return repo, nil
		}
	}
	provisioning, err := c.provisioningRepos(owner, "primary")
	if err != nil {
		return giteeRepo{}, err
	}
	for _, repoName := range provisioning {
		repo, found := byName[strings.ToLower(repoName)]
		if !found {
			if err := c.forgetProvisioningRepo(owner, repoName); err != nil {
				return giteeRepo{}, err
			}
			continue
		}
		if !repo.Private || !isOfferAtlasPrimaryName(repo.Name) {
			return giteeRepo{}, fmt.Errorf("等待初始化的同步仓库 %q 不符合 Offer Atlas 专用仓库规则", repo.Name)
		}
		if err := c.ensureRepoMarker(ctx, client, owner, repo.Name, syncFormatPath, "primary"); err != nil {
			return giteeRepo{}, fmt.Errorf("恢复 Gitee 同步仓库初始化: %w", err)
		}
		if err := c.forgetProvisioningRepo(owner, repo.Name); err != nil {
			return giteeRepo{}, err
		}
		return repo, nil
	}
	name := "offer-atlas-sync"
	for suffix := 0; ; suffix++ {
		candidate := name
		if suffix > 0 {
			candidate = fmt.Sprintf("%s-%03d", name, suffix+1)
		}
		found := false
		for _, repo := range repos {
			if strings.EqualFold(repo.Name, candidate) {
				found = true
				break
			}
		}
		if found {
			continue
		}
		repo, createErr := client.createPrivateRepo(ctx, candidate, "Offer Atlas multi-device synchronization")
		if createErr != nil {
			return giteeRepo{}, fmt.Errorf("创建 Gitee 私有同步仓库: %w", createErr)
		}
		if err := c.rememberProvisioningRepo(owner, repo.Name, "primary"); err != nil {
			return giteeRepo{}, fmt.Errorf("记录 Gitee 同步仓库初始化状态: %w", err)
		}
		if err := c.ensureRepoMarker(ctx, client, owner, repo.Name, syncFormatPath, "primary"); err != nil {
			return giteeRepo{}, fmt.Errorf("初始化 Gitee 同步仓库: %w", err)
		}
		if err := c.forgetProvisioningRepo(owner, repo.Name); err != nil {
			return giteeRepo{}, err
		}
		return repo, nil
	}
}

func isOfferAtlasPrimaryName(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "offer-atlas-sync" {
		return true
	}
	suffix, found := strings.CutPrefix(name, "offer-atlas-sync-")
	return found && isThreeDigitSequence(suffix)
}

func isOfferAtlasMediaName(name string) bool {
	suffix, found := strings.CutPrefix(strings.ToLower(strings.TrimSpace(name)), "offer-atlas-media-")
	return found && isThreeDigitSequence(suffix)
}

func isThreeDigitSequence(value string) bool {
	if len(value) != 3 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func (c *cloudSync) repoHasMarker(ctx context.Context, client *giteeClient, owner, repo, marker, kind string) bool {
	contents, _, err := client.readFile(ctx, owner, repo, marker)
	if err != nil {
		return false
	}
	return markerMatches(contents, kind)
}

func (c *cloudSync) ensureRepoMarker(ctx context.Context, client *giteeClient, owner, repo, markerPath, kind string) error {
	contents, sha, err := client.readFile(ctx, owner, repo, markerPath)
	if err == nil {
		if markerMatches(contents, kind) {
			return nil
		}
		return fmt.Errorf("仓库 %q 已有无效的 Offer Atlas 标识，已停止写入以保护该仓库", repo)
	}
	if !isGiteeNotFound(err) {
		return err
	}
	marker, marshalErr := json.Marshal(syncRepositoryMarker{
		Product: "Offer Atlas", Format: syncFormatVersion, Kind: kind, CreatedAt: nowString(),
	})
	if marshalErr != nil {
		return marshalErr
	}
	if err := client.writeFile(ctx, owner, repo, markerPath, "Initialize Offer Atlas "+kind+" repository", marker, sha); err == nil {
		return nil
	} else if existing, _, readErr := client.readFile(ctx, owner, repo, markerPath); readErr == nil && markerMatches(existing, kind) {
		// A response can be lost after a successful remote create. Reading back
		// the immutable marker makes this retry safe without a duplicate repo.
		return nil
	} else {
		return err
	}
}

func markerMatches(contents []byte, kind string) bool {
	marker, err := parseRepositoryMarker(contents, kind)
	return err == nil && marker.Product == "Offer Atlas" && marker.Format == syncFormatVersion && marker.Kind == kind
}

func parseRepositoryMarker(contents []byte, kind string) (syncRepositoryMarker, error) {
	var marker syncRepositoryMarker
	if err := json.Unmarshal(contents, &marker); err != nil {
		return syncRepositoryMarker{}, fmt.Errorf("标识文件不是有效 JSON")
	}
	if marker.Product != "Offer Atlas" || marker.Format != syncFormatVersion || marker.Kind != kind {
		return syncRepositoryMarker{}, fmt.Errorf("标识文件不是有效的 Offer Atlas %s 仓库声明", kind)
	}
	rawMinimumVersion := strings.TrimSpace(marker.MinimumClientVersion)
	marker.MinimumClientVersion = normalizeSyncVersion(rawMinimumVersion)
	if marker.MinimumClientVersion == "" && rawMinimumVersion != "" {
		return syncRepositoryMarker{}, fmt.Errorf("最低客户端版本格式无效")
	}
	marker.RequiredCapabilities = normalizedCapabilities(marker.RequiredCapabilities)
	if marker.CompatibilityEpoch < 0 {
		return syncRepositoryMarker{}, fmt.Errorf("兼容性版本号无效")
	}
	return marker, nil
}

func normalizeSyncVersion(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "v")
	value = strings.TrimPrefix(value, "V")
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return ""
	}
	for _, part := range parts {
		if part == "" {
			return ""
		}
		for _, character := range part {
			if character < '0' || character > '9' {
				return ""
			}
		}
	}
	return value
}

func syncVersionLess(left, right string) bool {
	left, right = normalizeSyncVersion(left), normalizeSyncVersion(right)
	if left == "" || right == "" {
		return false
	}
	leftParts, rightParts := strings.Split(left, "."), strings.Split(right, ".")
	for index := range leftParts {
		leftNumber, _ := strconv.Atoi(leftParts[index])
		rightNumber, _ := strconv.Atoi(rightParts[index])
		if leftNumber != rightNumber {
			return leftNumber < rightNumber
		}
	}
	return false
}

func normalizedCapabilities(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func (c *cloudSync) checkRemoteCompatibility(ctx context.Context, client *giteeClient, config syncConfigRow) (syncRepositoryMarker, string, error) {
	checkCtx, cancel := context.WithTimeout(ctx, compatibilityCheckTimeout)
	defer cancel()
	contents, sha, err := client.readFile(checkCtx, config.Owner, config.PrimaryRepo, syncFormatPath)
	if err != nil {
		return syncRepositoryMarker{}, "", &compatibilityCheckError{err: err}
	}
	marker, err := parseRepositoryMarker(contents, "primary")
	if err != nil {
		return syncRepositoryMarker{}, "", &compatibilityUnavailableError{message: "云端兼容性声明无效，已停止同步以保护本地数据：" + err.Error()}
	}
	if err := c.cacheRemoteCompatibility(marker); err != nil {
		return syncRepositoryMarker{}, "", fmt.Errorf("保存云端兼容性状态: %w", err)
	}
	if err := c.validateRemoteCompatibility(marker); err != nil {
		return marker, sha, err
	}
	return marker, sha, nil
}

func (c *cloudSync) cacheRemoteCompatibility(marker syncRepositoryMarker) error {
	capabilities, err := json.Marshal(normalizedCapabilities(marker.RequiredCapabilities))
	if err != nil {
		return err
	}
	_, err = c.store.db.Exec(`INSERT INTO sync_compatibility_cache(id, minimum_client_version, required_capabilities, compatibility_epoch, checked_at)
		VALUES (1, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET minimum_client_version=excluded.minimum_client_version, required_capabilities=excluded.required_capabilities, compatibility_epoch=excluded.compatibility_epoch, checked_at=excluded.checked_at`,
		marker.MinimumClientVersion, string(capabilities), marker.CompatibilityEpoch, nowString())
	return err
}

func (c *cloudSync) cachedRemoteCompatibility() (syncRepositoryMarker, string, error) {
	var marker syncRepositoryMarker
	var capabilities string
	var checkedAt string
	err := c.store.db.QueryRow(`SELECT minimum_client_version, required_capabilities, compatibility_epoch, checked_at FROM sync_compatibility_cache WHERE id=1`).Scan(
		&marker.MinimumClientVersion, &capabilities, &marker.CompatibilityEpoch, &checkedAt,
	)
	if err == sql.ErrNoRows {
		return syncRepositoryMarker{}, "", nil
	}
	if err != nil {
		return syncRepositoryMarker{}, "", err
	}
	if capabilities != "" && json.Unmarshal([]byte(capabilities), &marker.RequiredCapabilities) != nil {
		return syncRepositoryMarker{}, "", errors.New("本地云端兼容性缓存无效")
	}
	marker.RequiredCapabilities = normalizedCapabilities(marker.RequiredCapabilities)
	return marker, checkedAt, nil
}

func (c *cloudSync) validateRemoteCompatibility(marker syncRepositoryMarker) error {
	missing := missingSyncCapabilities(c.store.syncClient.Capabilities, marker.RequiredCapabilities)
	if (marker.MinimumClientVersion != "" && syncVersionLess(c.store.syncClient.Version, marker.MinimumClientVersion)) || len(missing) > 0 {
		return &compatibilityUpdateRequiredError{minimumVersion: marker.MinimumClientVersion, missingCapabilities: missing}
	}
	return nil
}

func missingSyncCapabilities(client, required []string) []string {
	have := make(map[string]bool, len(client))
	for _, capability := range client {
		have[capability] = true
	}
	missing := make([]string, 0)
	for _, capability := range normalizedCapabilities(required) {
		if !have[capability] {
			missing = append(missing, capability)
		}
	}
	return missing
}

func requirementsForOperations(operations []syncOperation) (string, []string) {
	minimumVersion := ""
	capabilities := []string{}
	for _, operation := range operations {
		spec, ok := syncSpec(operation.ObjectType)
		if !ok {
			continue
		}
		candidate := normalizeSyncVersion(spec.MinimumClientVersion)
		if candidate != "" && (minimumVersion == "" || syncVersionLess(minimumVersion, candidate)) {
			minimumVersion = candidate
		}
		capabilities = append(capabilities, spec.RequiredCapabilities...)
	}
	return minimumVersion, normalizedCapabilities(capabilities)
}

func (c *cloudSync) raiseRemoteCompatibilityForPending(ctx context.Context, client *giteeClient, config syncConfigRow, cutoffSequence int64) error {
	rows, err := c.store.db.Query(`SELECT object_type FROM sync_operations WHERE synced_at='' AND sequence<=?`, cutoffSequence)
	if err != nil {
		return err
	}
	defer rows.Close()
	operations := make([]syncOperation, 0)
	for rows.Next() {
		var objectType string
		if err := rows.Scan(&objectType); err != nil {
			return err
		}
		operations = append(operations, syncOperation{ObjectType: objectType})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(operations) == 0 {
		return nil
	}
	return c.raiseRemoteCompatibilityForOperations(ctx, client, config, operations)
}

// raiseRemoteCompatibilityForOperations raises requirements before publishing
// operations that need them. The marker is monotonic, so an older build can
// never be re-authorized by a later sync.
func (c *cloudSync) raiseRemoteCompatibilityForOperations(ctx context.Context, client *giteeClient, config syncConfigRow, operations []syncOperation) error {
	minimumVersion, capabilities := requirementsForOperations(operations)
	marker, sha, err := c.checkRemoteCompatibility(ctx, client, config)
	if err != nil {
		return err
	}
	changed := false
	if minimumVersion != "" && (marker.MinimumClientVersion == "" || syncVersionLess(marker.MinimumClientVersion, minimumVersion)) {
		marker.MinimumClientVersion = minimumVersion
		changed = true
	}
	combinedCapabilities := normalizedCapabilities(append(marker.RequiredCapabilities, capabilities...))
	if strings.Join(combinedCapabilities, "\x00") != strings.Join(marker.RequiredCapabilities, "\x00") {
		marker.RequiredCapabilities = combinedCapabilities
		changed = true
	}
	if !changed {
		return nil
	}
	marker.CompatibilityEpoch++
	marker.UpdatedAt = nowString()
	contents, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	if err := client.writeFile(ctx, config.Owner, config.PrimaryRepo, syncFormatPath, "Raise Offer Atlas sync compatibility requirement", contents, sha); err != nil {
		return fmt.Errorf("更新云端兼容性声明: %w", err)
	}
	return c.cacheRemoteCompatibility(marker)
}

func isGiteeNotFound(err error) bool {
	var apiErr *giteeError
	return errors.As(err, &apiErr) && apiErr.Status == 404
}

func (c *cloudSync) provisioningRepos(owner, kind string) ([]string, error) {
	rows, err := c.store.db.Query(`SELECT repo_name FROM sync_repo_provisioning WHERE owner=? AND kind=? ORDER BY created_at DESC`, owner, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var name string
		if rows.Scan(&name) == nil && strings.TrimSpace(name) != "" {
			result = append(result, name)
		}
	}
	return result, rows.Err()
}

func (c *cloudSync) rememberProvisioningRepo(owner, repo, kind string) error {
	_, err := c.store.db.Exec(`INSERT INTO sync_repo_provisioning(owner, repo_name, kind, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(owner, repo_name) DO UPDATE SET kind=excluded.kind, created_at=excluded.created_at`, owner, repo, kind, nowString())
	return err
}

func (c *cloudSync) forgetProvisioningRepo(owner, repo string) error {
	_, err := c.store.db.Exec(`DELETE FROM sync_repo_provisioning WHERE owner=? AND repo_name=?`, owner, repo)
	return err
}

func (c *cloudSync) remoteSummary(ctx context.Context, client *giteeClient, owner, repo string) (domain.SyncDataSummary, error) {
	operations, err := c.readRemoteOperations(ctx, client, owner, repo)
	if err != nil {
		return domain.SyncDataSummary{}, err
	}
	latest := map[string]syncOperation{}
	for _, operation := range operations {
		key := operation.ObjectType + ":" + operation.ObjectID
		if prior, ok := latest[key]; !ok || operation.ObjectVersion > prior.ObjectVersion || (operation.ObjectVersion == prior.ObjectVersion && operation.CreatedAt > prior.CreatedAt) {
			latest[key] = operation
		}
	}
	var result domain.SyncDataSummary
	for _, operation := range latest {
		if operation.Action == "delete" {
			continue
		}
		switch operation.ObjectType {
		case "company":
			result.Companies++
		case "campaign":
			result.Campaigns++
		case "position":
			result.Positions++
		case "application":
			result.Applications++
		case "stage":
			result.Stages++
		case "position_attachment", "resource_attachment":
			result.Attachments++
		case "resume", "application_resume":
			result.Resumes++
		}
	}
	return result, nil
}

// remoteHasSyncHistory is deliberately separate from remoteSummary. A summary
// can be empty even when the immutable log contains only deletes or no-op
// records. Such a repository is not safe to treat as a brand-new remote: its
// operation sequence must remain reserved, otherwise a fresh upload can reuse
// old paths and create an incomplete log for other devices.
func (c *cloudSync) remoteHasSyncHistory(ctx context.Context, client *giteeClient, owner, repo string) (bool, error) {
	entries, err := client.listDirectory(ctx, owner, repo, "operations")
	if err != nil {
		if isGiteeNotFound(err) {
			return false, nil
		}
		return false, err
	}
	for _, entry := range entries {
		if entry.Name != "" {
			return true, nil
		}
	}
	// A media index without operation files is still Offer Atlas sync data and
	// should not trigger a destructive ledger reset during first upload.
	entries, err = client.listDirectory(ctx, owner, repo, "media-index")
	if err != nil {
		if isGiteeNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return len(entries) > 0, nil
}

func (c *cloudSync) initializeTracking() error {
	return c.withImporting(func() error {
		return c.captureLocalChanges()
	})
}

func (c *cloudSync) captureLocalChanges() error {
	config, err := c.config()
	if err != nil {
		return err
	}
	if !config.Enabled {
		return nil
	}
	snapshots, err := c.collectSnapshots()
	if err != nil {
		return err
	}
	states, err := c.objectStates()
	if err != nil {
		return err
	}
	seen := make(map[string]bool, len(snapshots))
	for _, snapshot := range snapshots {
		seen[snapshot.Type+":"+snapshot.ID] = true
	}
	deletedStates := make([]syncObjectState, 0)
	for key, state := range states {
		if state.Deleted || seen[key] {
			continue
		}
		// Built-in stage types are intentionally omitted from snapshots. Keep
		// legacy tracking rows out of deletion detection as well, otherwise an
		// upgrade would emit a spurious delete for the old `other` catalog entry.
		if state.Type == "stage_type" && isBuiltInStageType(domain.StageType(state.ID)) {
			continue
		}
		deletedStates = append(deletedStates, state)
	}
	priority := map[string]int{}
	for index, spec := range syncObjectSpecs {
		priority[spec.Type] = index
	}
	sort.Slice(deletedStates, func(i, j int) bool {
		if deletedStates[i].Type == deletedStates[j].Type {
			return deletedStates[i].ID < deletedStates[j].ID
		}
		return priority[deletedStates[i].Type] > priority[deletedStates[j].Type]
	})
	deletedPayloads := make(map[string]json.RawMessage, len(deletedStates))
	for _, state := range deletedStates {
		payload, payloadErr := c.lastPayloadFor(state.Type, state.ID)
		if payloadErr != nil {
			return payloadErr
		}
		deletedPayloads[state.Type+":"+state.ID] = payload
	}
	tx, err := c.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	nextSequence := config.NextSequence
	now := nowString()
	for _, snapshot := range snapshots {
		key := snapshot.Type + ":" + snapshot.ID
		state, known := states[key]
		if known && !state.Deleted && state.ContentHash == snapshot.Hash {
			continue
		}
		version, base := int64(1), int64(0)
		if known {
			version, base = state.Version+1, state.Version
		}
		operation := syncOperation{ID: uuid.NewString(), DeviceID: config.DeviceID, Sequence: nextSequence, ObjectType: snapshot.Type, ObjectID: snapshot.ID, Action: "upsert", ObjectVersion: version, BaseVersion: base, Payload: snapshot.Payload, CreatedAt: now}
		if err := insertSyncOperation(tx, operation); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO sync_object_states(object_type, object_id, object_version, remote_version, content_hash, deleted, dirty, updated_at) VALUES (?, ?, ?, ?, ?, 0, 1, ?) ON CONFLICT(object_type, object_id) DO UPDATE SET object_version=excluded.object_version, content_hash=excluded.content_hash, deleted=0, dirty=1, updated_at=excluded.updated_at`, snapshot.Type, snapshot.ID, version, state.RemoteVersion, snapshot.Hash, now); err != nil {
			return err
		}
		nextSequence++
	}
	for _, state := range deletedStates {
		payload := deletedPayloads[state.Type+":"+state.ID]
		operation := syncOperation{ID: uuid.NewString(), DeviceID: config.DeviceID, Sequence: nextSequence, ObjectType: state.Type, ObjectID: state.ID, Action: "delete", ObjectVersion: state.Version + 1, BaseVersion: state.Version, Payload: payload, CreatedAt: now}
		if err := insertSyncOperation(tx, operation); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE sync_object_states SET object_version=?, deleted=1, dirty=1, updated_at=? WHERE object_type=? AND object_id=?`, operation.ObjectVersion, now, state.Type, state.ID); err != nil {
			return err
		}
		nextSequence++
	}
	if _, err := tx.Exec(`UPDATE sync_config SET next_sequence=?, updated_at=? WHERE id=1`, nextSequence, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (c *cloudSync) lastPayloadFor(objectType, objectID string) (json.RawMessage, error) {
	var payload string
	err := c.store.db.QueryRow(`SELECT payload FROM sync_operations WHERE object_type=? AND object_id=? AND action='upsert' ORDER BY sequence DESC LIMIT 1`, objectType, objectID).Scan(&payload)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return json.RawMessage(`{}`), nil
		}
		return nil, err
	}
	return json.RawMessage(payload), nil
}

func insertSyncOperation(tx *sql.Tx, operation syncOperation) error {
	_, err := tx.Exec(`INSERT INTO sync_operations(id, device_id, sequence, object_type, object_id, action, object_version, base_version, payload, created_at, synced_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '')`, operation.ID, operation.DeviceID, operation.Sequence, operation.ObjectType, operation.ObjectID, operation.Action, operation.ObjectVersion, operation.BaseVersion, string(operation.Payload), operation.CreatedAt)
	return err
}

func (c *cloudSync) objectStates() (map[string]syncObjectState, error) {
	rows, err := c.store.db.Query(`SELECT object_type, object_id, object_version, remote_version, content_hash, deleted, dirty, updated_at FROM sync_object_states`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	states := map[string]syncObjectState{}
	for rows.Next() {
		var item syncObjectState
		var deleted, dirty int
		if err := rows.Scan(&item.Type, &item.ID, &item.Version, &item.RemoteVersion, &item.ContentHash, &deleted, &dirty, &item.UpdatedAt); err != nil {
			return nil, err
		}
		item.Deleted, item.Dirty = deleted != 0, dirty != 0
		states[item.Type+":"+item.ID] = item
	}
	return states, rows.Err()
}

func (c *cloudSync) collectSnapshots() ([]syncSnapshot, error) {
	result := make([]syncSnapshot, 0)
	for _, spec := range syncObjectSpecs {
		rows, err := c.store.db.Query(`SELECT ` + strings.Join(spec.Columns, ",") + ` FROM ` + spec.Table)
		if err != nil {
			return nil, err
		}
		type rawSnapshot struct {
			payload map[string]any
			id      string
		}
		raw := make([]rawSnapshot, 0)
		for rows.Next() {
			payload, id, err := scanSyncPayload(rows, spec.Columns)
			if err != nil {
				rows.Close()
				return nil, err
			}
			raw = append(raw, rawSnapshot{payload: payload, id: id})
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		for _, item := range raw {
			payload, id := item.payload, item.id
			// Product-owned node types are recreated deterministically on every
			// device and never need network transport. The deprecated `other`
			// catalog entry is also seeded by older databases, but is not a
			// user-created type and must not become a first-sync local edit.
			if spec.Type == "stage_type" && isBuiltInStageType(domain.StageType(id)) {
				continue
			}
			if spec.FileKind != "" {
				if err := c.enrichFilePayload(spec, payload); err != nil {
					rows.Close()
					return nil, err
				}
			}
			encoded, err := json.Marshal(payload)
			if err != nil {
				rows.Close()
				return nil, err
			}
			hash := sha256.Sum256(encoded)
			result = append(result, syncSnapshot{Type: spec.Type, ID: id, Payload: encoded, Hash: hex.EncodeToString(hash[:])})
		}
	}
	priority := map[string]int{}
	for index, spec := range syncObjectSpecs {
		priority[spec.Type] = index
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Type == result[j].Type {
			return result[i].ID < result[j].ID
		}
		return priority[result[i].Type] < priority[result[j].Type]
	})
	return result, nil
}

func isBuiltInStageType(value domain.StageType) bool {
	return domain.IsSystemStageType(value) || value == domain.StageOther
}

func scanSyncPayload(rows *sql.Rows, columns []string) (map[string]any, string, error) {
	values := make([]any, len(columns))
	pointers := make([]any, len(columns))
	for index := range values {
		pointers[index] = &values[index]
	}
	if err := rows.Scan(pointers...); err != nil {
		return nil, "", err
	}
	payload := make(map[string]any, len(columns))
	for index, column := range columns {
		switch value := values[index].(type) {
		case []byte:
			payload[column] = string(value)
		default:
			payload[column] = value
		}
	}
	id, _ := payload["id"].(string)
	if id == "" {
		return nil, "", errors.New("sync object is missing id")
	}
	return payload, id, nil
}

func (c *cloudSync) enrichFilePayload(spec syncObjectSpec, payload map[string]any) error {
	if isSHA256Hash(stringValue(payload["content_hash"])) {
		return nil
	}
	if id := stringValue(payload["id"]); id != "" {
		hash, err := c.lastFileContentHash(spec.Type, id)
		if err != nil {
			return err
		}
		if hash != "" {
			payload["content_hash"] = hash
			return nil
		}
	}
	source, err := c.filePathForPayload(spec, payload)
	if err != nil {
		return err
	}
	digest, err := fileSHA256(source)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%s file is missing locally and has never been synchronized", fileKindLabel(spec.FileKind))
		}
		return fmt.Errorf("read %s attachment for sync: %w", spec.FileKind, err)
	}
	payload["content_hash"] = digest
	return nil
}

func (c *cloudSync) lastFileContentHash(objectType, objectID string) (string, error) {
	payload, err := c.lastPayloadFor(objectType, objectID)
	if err != nil {
		return "", err
	}
	var value map[string]any
	if err := json.Unmarshal(payload, &value); err != nil {
		return "", err
	}
	hash := stringValue(value["content_hash"])
	if !isSHA256Hash(hash) {
		return "", nil
	}
	return hash, nil
}

func isSHA256Hash(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func fileKindLabel(kind string) string {
	switch kind {
	case "resume":
		return "resume"
	case "position":
		return "position attachment"
	case "legacy_resume":
		return "application resume"
	case "resource":
		return "supplemental attachment"
	default:
		return "attachment"
	}
}

func (c *cloudSync) filePathForPayload(spec syncObjectSpec, payload map[string]any) (string, error) {
	if id := stringValue(payload["id"]); id != "" {
		switch spec.FileKind {
		case "position":
			var positionID, storedName string
			if queryErr := c.store.db.QueryRow(`SELECT position_id, stored_name FROM position_attachments WHERE id=?`, id).Scan(&positionID, &storedName); queryErr == nil {
				return c.store.attachmentPath(positionID, storedName)
			}
		case "resume":
			var storedName string
			if queryErr := c.store.db.QueryRow(`SELECT stored_name FROM resumes WHERE id=?`, id).Scan(&storedName); queryErr == nil && storedName != "" {
				return c.store.resumePath(storedName)
			}
		case "legacy_resume":
			var applicationID, storedName string
			if queryErr := c.store.db.QueryRow(`SELECT application_id, stored_name FROM application_resumes WHERE id=?`, id).Scan(&applicationID, &storedName); queryErr == nil {
				return c.store.applicationResumePath(applicationID, storedName)
			}
		case "resource":
			var ownerType, ownerID, storedName string
			if queryErr := c.store.db.QueryRow(`SELECT owner_type, owner_id, stored_name FROM supplemental_attachments WHERE id=?`, id).Scan(&ownerType, &ownerID, &storedName); queryErr == nil {
				return c.store.supplementalAttachmentPath(domain.ResourceOwnerType(ownerType), ownerID, storedName)
			}
		}
	}
	return c.rawFilePathForPayload(spec, payload)
}

func (c *cloudSync) rawFilePathForPayload(spec syncObjectSpec, payload map[string]any) (string, error) {
	switch spec.FileKind {
	case "position":
		return c.store.attachmentPath(stringValue(payload["position_id"]), stringValue(payload["stored_name"]))
	case "resume":
		storedName := stringValue(payload["stored_name"])
		if storedName == "" {
			hash := stringValue(payload["content_hash"])
			if !isSHA256Hash(hash) {
				return "", errors.New("remote resume metadata is missing a valid filename and content hash")
			}
			storedName = hash + attachmentExtension(strings.ToLower(stringValue(payload["mime_type"])))
		}
		return c.store.resumePath(storedName)
	case "legacy_resume":
		return c.store.applicationResumePath(stringValue(payload["application_id"]), stringValue(payload["stored_name"]))
	case "resource":
		return c.store.supplementalAttachmentPath(domain.ResourceOwnerType(stringValue(payload["owner_type"])), stringValue(payload["owner_id"]), stringValue(payload["stored_name"]))
	default:
		return "", fmt.Errorf("unknown attachment kind %q", spec.FileKind)
	}
}

func stringValue(value any) string { result, _ := value.(string); return result }

func fileSHA256(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (c *cloudSync) readRemoteOperations(ctx context.Context, client *giteeClient, owner, repo string) ([]syncOperation, error) {
	devices, err := client.listDirectory(ctx, owner, repo, "operations")
	if err != nil {
		var apiErr *giteeError
		if errors.As(err, &apiErr) && apiErr.Status == 404 {
			return []syncOperation{}, nil
		}
		return nil, err
	}
	result := make([]syncOperation, 0)
	for _, device := range devices {
		if device.Type != "dir" || device.Name == "" {
			continue
		}
		files, listErr := client.listDirectory(ctx, owner, repo, "operations/"+device.Name)
		if listErr != nil {
			return nil, listErr
		}
		for _, file := range files {
			if file.Type != "file" || !strings.HasSuffix(file.Name, ".json") {
				continue
			}
			if file.Name == operationIndexName {
				continue
			}
			data, _, readErr := client.readFile(ctx, owner, repo, file.Path)
			if readErr != nil {
				return nil, readErr
			}
			var operation syncOperation
			if err := json.Unmarshal(data, &operation); err != nil {
				return nil, fmt.Errorf("read remote sync operation %s: %w", file.Path, err)
			}
			if operation.ID == "" || operation.ObjectType == "" || operation.ObjectID == "" || operation.DeviceID == "" || !validSyncAction(operation.Action) {
				return nil, fmt.Errorf("remote operation %s has invalid content", file.Path)
			}
			result = append(result, operation)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt == result[j].CreatedAt {
			if result[i].DeviceID == result[j].DeviceID {
				return result[i].Sequence < result[j].Sequence
			}
			return result[i].DeviceID < result[j].DeviceID
		}
		return result[i].CreatedAt < result[j].CreatedAt
	})
	return result, nil
}

func (c *cloudSync) remoteOperationFilesSince(ctx context.Context, client *giteeClient, owner, repo, deviceID string, cursor int64) ([]remoteOperationFile, error) {
	indexPath := "operations/" + deviceID + "/" + operationIndexName
	indexData, _, indexErr := client.readFile(ctx, owner, repo, indexPath)
	if indexErr == nil {
		var index remoteOperationIndex
		if err := json.Unmarshal(indexData, &index); err != nil {
			return nil, fmt.Errorf("read remote operation index %s: %w", indexPath, err)
		}
		if !index.validFor(deviceID) {
			return nil, fmt.Errorf("remote operation index %s has invalid content", indexPath)
		}
		// Existing installations have historical UUID-suffixed filenames. List
		// those only until their one-time import has reached the canonical range.
		if cursor >= index.CanonicalFrom-1 {
			files := make([]remoteOperationFile, 0)
			for sequence := cursor + 1; sequence <= index.LatestSequence; sequence++ {
				files = append(files, remoteOperationFile{file: giteeContent{Name: fmt.Sprintf("%020d.json", sequence), Path: fmt.Sprintf("operations/%s/%020d.json", deviceID, sequence), Type: "file"}, sequence: sequence, knownSeq: true})
			}
			return files, nil
		}
	} else if !isGiteeNotFound(indexErr) {
		return nil, indexErr
	}
	files, err := client.listDirectory(ctx, owner, repo, "operations/"+deviceID)
	if err != nil {
		return nil, err
	}
	return selectRemoteOperationFiles(files, cursor), nil
}

// recoverRemoteOperationCandidate handles repositories that have a valid
// operation index but still contain legacy filenames. The index is useful for
// the common path; only a missing canonical file pays the extra directory/read
// cost here.
func (c *cloudSync) recoverRemoteOperationCandidate(ctx context.Context, client *giteeClient, owner, repo string, candidate remoteOperationCandidate) (remoteOperationCandidate, error) {
	if !candidate.knownSeq || candidate.sequence <= 0 {
		return remoteOperationCandidate{}, errors.New("remote operation has no recoverable sequence")
	}
	repoNames := []string{repo}
	seenRepos := map[string]bool{repo: true}
	// Early builds could create more than one Offer Atlas primary repository.
	// The configured repository remains authoritative; this is only a recovery
	// path for an immutable operation that it claims exists but cannot provide.
	if repos, listErr := client.listRepos(ctx); listErr == nil {
		for _, listed := range repos {
			if listed.Private && isOfferAtlasPrimaryName(listed.Name) && !seenRepos[listed.Name] {
				seenRepos[listed.Name] = true
				repoNames = append(repoNames, listed.Name)
			}
		}
	}
	var lastErr error
	for _, repoName := range repoNames {
		files, listErr := client.listDirectory(ctx, owner, repoName, "operations/"+candidate.deviceID)
		if listErr != nil {
			lastErr = listErr
			continue
		}
		if recovered, ok := findOperationCandidateInFiles(ctx, client, owner, repoName, candidate, files); ok {
			return recovered, nil
		}
	}
	if lastErr != nil {
		return remoteOperationCandidate{}, lastErr
	}
	return remoteOperationCandidate{}, fmt.Errorf("remote operation sequence %d was not found in Offer Atlas primary repositories", candidate.sequence)
}

func findOperationCandidateInFiles(ctx context.Context, client *giteeClient, owner, repo string, candidate remoteOperationCandidate, files []giteeContent) (remoteOperationCandidate, bool) {
	for _, file := range files {
		if file.Type != "file" || file.Name == operationIndexName || !strings.HasSuffix(file.Name, ".json") {
			continue
		}
		if sequence, ok := operationSequenceFromFilename(file.Name); ok && sequence == candidate.sequence {
			return remoteOperationCandidate{repo: repo, deviceID: candidate.deviceID, remoteOperationFile: remoteOperationFile{file: file, sequence: sequence, knownSeq: true}}, true
		}
	}
	// The oldest format used UUID-only filenames. Read those only on this rare
	// recovery path and match their embedded operation sequence.
	for _, file := range files {
		if file.Type != "file" || file.Name == operationIndexName || !strings.HasSuffix(file.Name, ".json") {
			continue
		}
		if _, ok := operationSequenceFromFilename(file.Name); ok {
			continue
		}
		data, _, readErr := client.readFile(ctx, owner, repo, file.Path)
		if readErr != nil {
			continue
		}
		var operation syncOperation
		if json.Unmarshal(data, &operation) == nil && operation.Sequence == candidate.sequence && operation.DeviceID == candidate.deviceID {
			return remoteOperationCandidate{repo: repo, deviceID: candidate.deviceID, remoteOperationFile: remoteOperationFile{file: file, sequence: candidate.sequence, knownSeq: true}}, true
		}
	}
	return remoteOperationCandidate{}, false
}

func (index remoteOperationIndex) validFor(deviceID string) bool {
	return index.Product == "Offer Atlas" && index.Format == syncFormatVersion && index.DeviceID == deviceID && index.CanonicalFrom > 0 && index.LatestSequence >= index.CanonicalFrom-1
}

func selectRemoteOperationFiles(files []giteeContent, cursor int64) []remoteOperationFile {
	known := make([]remoteOperationFile, 0, len(files))
	legacy := make([]remoteOperationFile, 0)
	for _, file := range files {
		if file.Type != "file" || !strings.HasSuffix(file.Name, ".json") {
			continue
		}
		if file.Name == operationIndexName {
			continue
		}
		if sequence, ok := operationSequenceFromFilename(file.Name); ok {
			if sequence > cursor {
				known = append(known, remoteOperationFile{file: file, sequence: sequence, knownSeq: true})
			}
			continue
		}
		// Older operation names were not required to encode a sequence. Read
		// those conservatively and let the durable applied-ID table de-duplicate.
		legacy = append(legacy, remoteOperationFile{file: file})
	}
	sort.Slice(known, func(i, j int) bool { return known[i].sequence < known[j].sequence })
	selected := append([]remoteOperationFile{}, legacy...)
	expected := cursor + 1
	for _, file := range known {
		if file.sequence < expected {
			continue
		}
		if file.sequence != expected {
			// Do not apply a later operation before an earlier one that was absent
			// from this listing. A subsequent check will resume at the same cursor.
			break
		}
		selected = append(selected, file)
		expected++
	}
	return selected
}

func operationSequenceFromFilename(name string) (int64, bool) {
	name = strings.TrimSuffix(name, ".json")
	separator := strings.IndexByte(name, '-')
	if separator < 0 {
		sequence, err := strconv.ParseInt(name, 10, 64)
		return sequence, err == nil && sequence > 0
	}
	if separator == 0 {
		return 0, false
	}
	sequence, err := strconv.ParseInt(name[:separator], 10, 64)
	if err != nil || sequence <= 0 {
		return 0, false
	}
	return sequence, true
}

func (c *cloudSync) pullRemote(ctx context.Context, client *giteeClient, config syncConfigRow) error {
	cursors, err := c.remoteCursors()
	if err != nil {
		return err
	}
	candidates, err := c.remoteOperationCandidatesSince(ctx, client, config.Owner, config.PrimaryRepo, cursors)
	if err != nil {
		return err
	}
	if len(candidates) == 0 {
		return nil
	}
	seen, err := c.appliedOperationIDs()
	if err != nil {
		return err
	}
	c.setActivityProgress("downloading", len(candidates), 0)
	cursorsProgress := newRemoteCursorProgress(c, cursors)
	legacyResumeReceived := false
	deferred := make([]deferredRemoteOperation, 0)
	apply := func(operation syncOperation, sequence int64, file bool) (bool, error) {
		if err := c.applyRemoteOperation(ctx, client, config, operation); err != nil {
			if isForeignKeyConstraintError(err) {
				return false, nil
			}
			return false, err
		}
		if operation.ObjectType == "application_resume" && operation.Action == "upsert" {
			legacyResumeReceived = true
		}
		seen[operation.ID] = true
		if err := cursorsProgress.complete(operation.DeviceID, sequence); err != nil {
			return false, err
		}
		c.advanceActivityProgress(file)
		return true, nil
	}
	for _, candidate := range candidates {
		operationRepo := candidate.repo
		if operationRepo == "" {
			operationRepo = config.PrimaryRepo
		}
		data, _, readErr := client.readFile(ctx, config.Owner, operationRepo, candidate.file.Path)
		if readErr != nil && isGiteeNotFound(readErr) && candidate.knownSeq {
			recovered, recoverErr := c.recoverRemoteOperationCandidate(ctx, client, config.Owner, config.PrimaryRepo, candidate)
			if recoverErr == nil {
				candidate = recovered
				operationRepo = candidate.repo
				data, _, readErr = client.readFile(ctx, config.Owner, operationRepo, candidate.file.Path)
			} else {
				return &remoteSyncIntegrityError{message: fmt.Sprintf("远端同步日志不完整：设备 %s 的第 %d 条操作在当前及其他 Offer Atlas 主仓库中都找不到（原路径 %s/%s）；%v", candidate.deviceID, candidate.sequence, operationRepo, candidate.file.Path, recoverErr)}
			}
		}
		if readErr != nil {
			return fmt.Errorf("read remote sync operation %s/%s: %w", operationRepo, candidate.file.Path, readErr)
		}
		var operation syncOperation
		if err := json.Unmarshal(data, &operation); err != nil {
			return fmt.Errorf("read remote sync operation %s: %w", candidate.file.Path, err)
		}
		if operation.ID == "" || operation.ObjectType == "" || operation.ObjectID == "" || operation.DeviceID == "" || !validSyncAction(operation.Action) {
			return fmt.Errorf("remote operation %s has invalid content", candidate.file.Path)
		}
		if operation.DeviceID != candidate.deviceID {
			return fmt.Errorf("remote operation %s is stored under the wrong device directory", candidate.file.Path)
		}
		if candidate.knownSeq && operation.Sequence != candidate.sequence {
			return fmt.Errorf("remote operation %s has a filename sequence that does not match its content", candidate.file.Path)
		}
		sequence := candidate.sequence
		if !candidate.knownSeq {
			sequence = operation.Sequence
		}
		if seen[operation.ID] || operation.DeviceID == config.DeviceID {
			if err := cursorsProgress.complete(operation.DeviceID, sequence); err != nil {
				return err
			}
			c.advanceActivityProgress(false)
			continue
		}
		file := false
		if spec, ok := syncSpec(operation.ObjectType); ok && spec.FileKind != "" && operation.Action == "upsert" {
			file = true
			c.addActivityFileTotal()
		}
		applied, err := apply(operation, sequence, file)
		if err != nil {
			return err
		}
		if !applied {
			deferred = append(deferred, deferredRemoteOperation{operation: operation, sequence: sequence, file: file})
		}
	}
	for len(deferred) > 0 {
		next := make([]deferredRemoteOperation, 0, len(deferred))
		progressed := false
		for _, pending := range deferred {
			applied, err := apply(pending.operation, pending.sequence, pending.file)
			if err != nil {
				return err
			}
			if applied {
				progressed = true
				continue
			}
			next = append(next, pending)
		}
		if !progressed {
			return remoteDependencyError(next)
		}
		deferred = next
	}
	if legacyResumeReceived {
		if err := c.store.migrateLegacyApplicationResumes(); err != nil {
			return err
		}
		// Conversion created reusable resume objects and updated applications.
		// Record them for the next batch; the current cutoff remains immutable.
		if err := c.captureLocalChanges(); err != nil {
			return err
		}
	}
	return nil
}

func isForeignKeyConstraintError(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "foreign key constraint failed")
}

func remoteDependencyError(pending []deferredRemoteOperation) error {
	items := make([]string, 0, len(pending))
	for _, item := range pending {
		items = append(items, fmt.Sprintf("%s (%s)", item.operation.ObjectType, item.operation.ObjectID))
		if len(items) == 3 {
			break
		}
	}
	return fmt.Errorf("云端数据缺少依赖对象，无法完成恢复：%s", strings.Join(items, "、"))
}

// remoteOperationCandidatesSince discovers pending file paths without reading
// their JSON content. pullRemote then reads, imports and checkpoints one file
// at a time, which makes an interrupted first download genuinely resumable.
func (c *cloudSync) remoteOperationCandidatesSince(ctx context.Context, client *giteeClient, owner, repo string, cursors map[string]int64) ([]remoteOperationCandidate, error) {
	devices, err := client.listDirectory(ctx, owner, repo, "operations")
	if err != nil {
		var apiErr *giteeError
		if errors.As(err, &apiErr) && apiErr.Status == 404 {
			return []remoteOperationCandidate{}, nil
		}
		return nil, err
	}
	deviceIDs := make([]string, 0, len(devices))
	for _, device := range devices {
		if device.Type == "dir" && device.Name != "" {
			deviceIDs = append(deviceIDs, device.Name)
		}
	}
	sort.Strings(deviceIDs)
	result := make([]remoteOperationCandidate, 0)
	for _, deviceID := range deviceIDs {
		files, err := c.remoteOperationFilesSince(ctx, client, owner, repo, deviceID, cursors[deviceID])
		if err != nil {
			return nil, err
		}
		for _, file := range files {
			result = append(result, remoteOperationCandidate{repo: repo, deviceID: deviceID, remoteOperationFile: file})
		}
	}
	return result, nil
}

// remoteCursorProgress records a durable checkpoint after each successfully
// imported operation. A failed first download therefore resumes from the next
// operation, not from the beginning of a potentially large history. Sequence
// gaps are never crossed: only a contiguous prefix is persisted per device.
type remoteCursorProgress struct {
	cloud     *cloudSync
	cursors   map[string]int64
	completed map[string]map[int64]bool
}

func newRemoteCursorProgress(cloud *cloudSync, cursors map[string]int64) *remoteCursorProgress {
	current := make(map[string]int64, len(cursors))
	for device, cursor := range cursors {
		current[device] = cursor
	}
	return &remoteCursorProgress{cloud: cloud, cursors: current, completed: map[string]map[int64]bool{}}
}

func (p *remoteCursorProgress) complete(device string, sequence int64) error {
	if sequence <= 0 {
		return nil
	}
	if p.completed[device] == nil {
		p.completed[device] = map[int64]bool{}
	}
	p.completed[device][sequence] = true
	cursor := p.cursors[device]
	for p.completed[device][cursor+1] {
		cursor++
	}
	if cursor == p.cursors[device] {
		return nil
	}
	if _, err := p.cloud.store.db.Exec(`INSERT INTO sync_remote_cursors(device_id, last_sequence, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(device_id) DO UPDATE SET last_sequence=excluded.last_sequence, updated_at=excluded.updated_at`, device, cursor, nowString()); err != nil {
		return err
	}
	p.cursors[device] = cursor
	return nil
}

func (c *cloudSync) remoteCursors() (map[string]int64, error) {
	rows, err := c.store.db.Query(`SELECT device_id, last_sequence FROM sync_remote_cursors`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]int64{}
	for rows.Next() {
		var device string
		var sequence int64
		if err := rows.Scan(&device, &sequence); err != nil {
			return nil, err
		}
		result[device] = sequence
	}
	return result, rows.Err()
}

func (c *cloudSync) appliedOperationIDs() (map[string]bool, error) {
	rows, err := c.store.db.Query(`SELECT operation_id FROM sync_applied_operations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result[id] = true
	}
	return result, rows.Err()
}

func (c *cloudSync) applyRemoteOperation(ctx context.Context, client *giteeClient, config syncConfigRow, remote syncOperation) error {
	if remote.Action == "noop" {
		return c.markOperationApplied(remote.ID)
	}
	states, err := c.objectStates()
	if err != nil {
		return err
	}
	key := remote.ObjectType + ":" + remote.ObjectID
	local, hasLocal := states[key]
	if hasLocal {
		var openConflict int
		if err := c.store.db.QueryRow(`SELECT COUNT(*) FROM sync_conflicts WHERE status='open' AND object_type=? AND object_id=?`, remote.ObjectType, remote.ObjectID).Scan(&openConflict); err != nil {
			return err
		}
		if openConflict > 0 {
			// Keep the object frozen until the user decides. Later remote edits
			// are preserved as additional conflict entries instead of silently
			// overwriting the local branch or being uploaded out of order.
			if err := c.createConflict(local, remote); err != nil {
				return err
			}
			return c.markOperationApplied(remote.ID)
		}
	}
	if hasLocal && local.Dirty && local.Version > remote.BaseVersion {
		if err := c.createConflict(local, remote); err != nil {
			return err
		}
		return c.markOperationApplied(remote.ID)
	}
	if hasLocal && remote.ObjectVersion <= local.RemoteVersion {
		return c.markOperationApplied(remote.ID)
	}
	if remote.Action == "upsert" && len(remote.Payload) == 0 {
		return fmt.Errorf("remote upsert %s has no payload", remote.ID)
	}
	if remote.Action == "upsert" {
		normalized, err := normalizeRemoteFilePayload(remote.ObjectType, remote.Payload)
		if err != nil {
			return err
		}
		remote.Payload = normalized
	}
	return c.withImporting(func() error {
		// File transfer may consult the media index. Do it before opening the
		// single-connection SQLite transaction, then atomically register its
		// metadata with the corresponding business object below.
		if remote.Action == "upsert" {
			if err := c.materializeRemoteFile(ctx, client, config, remote); err != nil {
				return err
			}
		}
		tx, err := c.store.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var affectedApplicationID string
		if remote.Action == "delete" {
			affectedApplicationID = applicationIDFromPayload(remote.ObjectType, remote.Payload)
			if err := deleteSyncedObject(tx, remote.ObjectType, remote.ObjectID); err != nil {
				return err
			}
		} else {
			if err := upsertSyncedObject(tx, remote.ObjectType, remote.Payload); err != nil {
				return err
			}
		}
		hash := ""
		if remote.Action == "upsert" {
			hash = contentHash(remote.Payload)
		}
		if _, err := tx.Exec(`INSERT INTO sync_object_states(object_type, object_id, object_version, remote_version, content_hash, deleted, dirty, updated_at) VALUES (?, ?, ?, ?, ?, ?, 0, ?) ON CONFLICT(object_type, object_id) DO UPDATE SET object_version=MAX(sync_object_states.object_version, excluded.object_version), remote_version=excluded.remote_version, content_hash=excluded.content_hash, deleted=excluded.deleted, dirty=0, updated_at=excluded.updated_at`, remote.ObjectType, remote.ObjectID, remote.ObjectVersion, remote.ObjectVersion, hash, boolToInt(remote.Action == "delete"), remote.CreatedAt); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO sync_applied_operations(operation_id, applied_at) VALUES (?, ?)`, remote.ID, nowString()); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		if remote.ObjectType == "stage" {
			if affectedApplicationID == "" {
				affectedApplicationID = applicationIDFromPayload(remote.ObjectType, remote.Payload)
			}
			if affectedApplicationID != "" {
				_ = c.store.refreshApplicationStatus(affectedApplicationID)
			}
		}
		if remote.Action == "delete" {
			if err := c.cleanupDeletedRemoteFile(remote.ObjectType, remote.Payload); err != nil {
				return err
			}
		}
		return nil
	})
}

func validSyncAction(action string) bool {
	return action == "upsert" || action == "delete" || action == "noop"
}

func normalizeRemoteFilePayload(objectType string, payload json.RawMessage) (json.RawMessage, error) {
	spec, ok := syncSpec(objectType)
	if !ok || spec.FileKind == "" {
		return payload, nil
	}
	value := map[string]any{}
	if err := json.Unmarshal(payload, &value); err != nil {
		return nil, err
	}
	if spec.FileKind == "resume" && stringValue(value["stored_name"]) == "" {
		hash := stringValue(value["content_hash"])
		if !isSHA256Hash(hash) {
			return nil, fmt.Errorf("remote resume %s is missing stored_name and a valid content_hash", stringValue(value["id"]))
		}
		value["stored_name"] = hash + attachmentExtension(strings.ToLower(stringValue(value["mime_type"])))
	}
	normalized, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return normalized, nil
}

func applicationIDFromPayload(objectType string, payload json.RawMessage) string {
	if objectType != "stage" {
		return ""
	}
	var value map[string]any
	if json.Unmarshal(payload, &value) != nil {
		return ""
	}
	return stringValue(value["application_id"])
}

func (c *cloudSync) cleanupDeletedRemoteFile(objectType string, payload json.RawMessage) error {
	spec, ok := syncSpec(objectType)
	if !ok || spec.FileKind == "" || len(payload) == 0 {
		return nil
	}
	var value map[string]any
	if json.Unmarshal(payload, &value) != nil {
		return nil
	}
	if spec.FileKind == "resume" && stringValue(value["stored_name"]) == "" && !isSHA256Hash(stringValue(value["content_hash"])) {
		// Historical delete records may not contain enough metadata to locate a
		// file. The database row has already been removed, so cleanup is safely
		// idempotent and can be skipped.
		return nil
	}
	target, err := c.filePathForPayload(spec, value)
	if err != nil {
		return err
	}
	if spec.FileKind == "resume" {
		var remaining int
		if err := c.store.db.QueryRow(`SELECT COUNT(*) FROM resumes WHERE stored_name=?`, stringValue(value["stored_name"])).Scan(&remaining); err != nil {
			return err
		}
		if remaining > 0 {
			return nil
		}
	}
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func contentHash(payload []byte) string {
	hash := sha256.Sum256(payload)
	return hex.EncodeToString(hash[:])
}
func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (c *cloudSync) markOperationApplied(id string) error {
	_, err := c.store.db.Exec(`INSERT OR IGNORE INTO sync_applied_operations(operation_id, applied_at) VALUES (?, ?)`, id, nowString())
	return err
}

func (c *cloudSync) createConflict(local syncObjectState, remote syncOperation) error {
	var localPayload string
	if snapshot, err := c.snapshotFor(local.Type, local.ID); err == nil {
		localPayload = string(snapshot.Payload)
	} else {
		payload, payloadErr := c.lastPayloadFor(local.Type, local.ID)
		if payloadErr != nil {
			return payloadErr
		}
		localPayload = string(payload)
	}
	result, err := c.store.db.Exec(`UPDATE sync_conflicts SET local_payload=?, local_updated_at=?, remote_payload=?, remote_updated_at=?, remote_operation_id=?, remote_action=?, remote_version=? WHERE object_type=? AND object_id=? AND status='open'`, localPayload, local.UpdatedAt, string(remote.Payload), remote.CreatedAt, remote.ID, remote.Action, remote.ObjectVersion, local.Type, local.ID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected > 0 {
		return nil
	}
	_, err = c.store.db.Exec(`INSERT INTO sync_conflicts(id, object_type, object_id, local_payload, remote_payload, local_updated_at, remote_updated_at, remote_operation_id, status, created_at, remote_action, remote_version) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'open', ?, ?, ?)`, uuid.NewString(), local.Type, local.ID, localPayload, string(remote.Payload), local.UpdatedAt, remote.CreatedAt, remote.ID, nowString(), remote.Action, remote.ObjectVersion)
	return err
}

func (c *cloudSync) snapshotFor(objectType, id string) (syncSnapshot, error) {
	spec, ok := syncSpec(objectType)
	if !ok {
		return syncSnapshot{}, fmt.Errorf("unknown sync object type %q", objectType)
	}
	row := c.store.db.QueryRow(`SELECT `+strings.Join(spec.Columns, ",")+` FROM `+spec.Table+` WHERE id=?`, id)
	values := make([]any, len(spec.Columns))
	pointers := make([]any, len(spec.Columns))
	for index := range values {
		pointers[index] = &values[index]
	}
	if err := row.Scan(pointers...); err != nil {
		return syncSnapshot{}, err
	}
	payload := map[string]any{}
	for index, column := range spec.Columns {
		if data, ok := values[index].([]byte); ok {
			payload[column] = string(data)
		} else {
			payload[column] = values[index]
		}
	}
	if spec.FileKind != "" {
		if err := c.enrichFilePayload(spec, payload); err != nil {
			return syncSnapshot{}, err
		}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return syncSnapshot{}, err
	}
	return syncSnapshot{Type: objectType, ID: id, Payload: encoded, Hash: contentHash(encoded)}, nil
}

func deleteSyncedObject(tx *sql.Tx, objectType, id string) error {
	spec, ok := syncSpec(objectType)
	if !ok {
		return fmt.Errorf("unknown sync object type %q", objectType)
	}
	// Parent deletion is allowed to cascade; a child may already be absent after
	// a preceding remote deletion, which is intentionally idempotent.
	_, err := tx.Exec(`DELETE FROM `+spec.Table+` WHERE id=?`, id)
	return err
}

func upsertSyncedObject(tx *sql.Tx, objectType string, payload json.RawMessage) error {
	spec, ok := syncSpec(objectType)
	if !ok {
		return fmt.Errorf("unknown sync object type %q", objectType)
	}
	values := map[string]any{}
	if err := json.Unmarshal(payload, &values); err != nil {
		return err
	}
	args := make([]any, len(spec.Columns))
	for index, column := range spec.Columns {
		value, exists := values[column]
		if !exists {
			// Operations written before the reusable resume library did not have
			// this nullable association. Treat it as an empty link on import.
			if objectType == "application" && column == "resume_id" {
				args[index] = nil
				continue
			}
			return fmt.Errorf("remote %s payload is missing %s", objectType, column)
		}
		args[index] = normalizeJSONSQLite(value)
	}
	updates := make([]string, 0, len(spec.Columns)-1)
	for _, column := range spec.Columns[1:] {
		updates = append(updates, column+"=excluded."+column)
	}
	_, err := tx.Exec(`INSERT INTO `+spec.Table+`(`+strings.Join(spec.Columns, ",")+`) VALUES (`+placeholders(len(spec.Columns))+`) ON CONFLICT(id) DO UPDATE SET `+strings.Join(updates, ","), args...)
	return err
}

func normalizeJSONSQLite(value any) any {
	if value == nil {
		return nil
	}
	if number, ok := value.(float64); ok {
		return int64(number)
	}
	return value
}

func placeholders(count int) string { return strings.TrimRight(strings.Repeat("?,", count), ",") }

func (c *cloudSync) materializeRemoteFile(ctx context.Context, client *giteeClient, config syncConfigRow, operation syncOperation) error {
	spec, ok := syncSpec(operation.ObjectType)
	if !ok || spec.FileKind == "" || operation.Action != "upsert" {
		return nil
	}
	payload := map[string]any{}
	if err := json.Unmarshal(operation.Payload, &payload); err != nil {
		return err
	}
	hash := stringValue(payload["content_hash"])
	if hash == "" {
		return fmt.Errorf("remote attachment %s has no content hash", operation.ID)
	}
	var repoName, remotePath string
	err := c.store.db.QueryRow(`SELECT repo_name, remote_path FROM sync_media_files WHERE content_hash=?`, hash).Scan(&repoName, &remotePath)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			index, _, indexErr := client.readFile(ctx, config.Owner, config.PrimaryRepo, "media-index/"+hash+".json")
			if indexErr == nil {
				var entry struct {
					RepoName   string `json:"repoName"`
					RemotePath string `json:"remotePath"`
					SizeBytes  int64  `json:"sizeBytes"`
				}
				if json.Unmarshal(index, &entry) != nil || entry.RepoName == "" || entry.RemotePath == "" {
					return fmt.Errorf("remote attachment %s has invalid media metadata", hash[:12])
				}
				repoName, remotePath = entry.RepoName, entry.RemotePath
				if _, saveErr := c.store.db.Exec(`INSERT OR IGNORE INTO sync_media_files(content_hash, repo_name, remote_path, size_bytes, updated_at) VALUES (?, ?, ?, ?, ?)`, hash, repoName, remotePath, entry.SizeBytes, nowString()); saveErr != nil {
					return saveErr
				}
			} else if isGiteeNotFound(indexErr) {
				// Older repositories may contain media files without the primary
				// media-index entry. Recover by probing the deterministic media path.
				repoName, remotePath, err = c.findRemoteMediaFile(ctx, client, config, hash)
				if err != nil {
					return fmt.Errorf("look up remote attachment %s: %w", hash[:12], err)
				}
			} else {
				return fmt.Errorf("look up remote attachment %s: %w", hash[:12], indexErr)
			}
		} else {
			return err
		}
	}
	contents, _, err := client.readFile(ctx, config.Owner, repoName, remotePath)
	if err != nil && isGiteeNotFound(err) {
		// Older sync versions could leave a stale media-index entry (or a local
		// cache pointing at a directory). The content hash is authoritative, so
		// probe the deterministic media paths before failing the whole sync run.
		if recoveredRepo, recoveredPath, probeErr := c.findRemoteMediaFile(ctx, client, config, hash); probeErr == nil {
			repoName, remotePath = recoveredRepo, recoveredPath
			contents, _, err = client.readFile(ctx, config.Owner, repoName, remotePath)
		}
	}
	if err != nil {
		return fmt.Errorf("read remote attachment %s (%s/%s): %w", hash[:12], repoName, remotePath, err)
	}
	actual := sha256.Sum256(contents)
	if hex.EncodeToString(actual[:]) != hash {
		return fmt.Errorf("remote attachment hash verification failed")
	}
	// Keep the recovered path and its real size locally. This also repairs a
	// stale cache entry so later syncs do not probe the broken path again.
	if _, saveErr := c.store.db.Exec(`INSERT INTO sync_media_files(content_hash, repo_name, remote_path, size_bytes, updated_at) VALUES (?, ?, ?, ?, ?) ON CONFLICT(content_hash) DO UPDATE SET repo_name=excluded.repo_name, remote_path=excluded.remote_path, size_bytes=excluded.size_bytes, updated_at=excluded.updated_at`, hash, repoName, remotePath, len(contents), nowString()); saveErr != nil {
		return saveErr
	}
	destination, err := c.filePathForPayload(spec, payload)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	return writeSyncedFile(destination, contents)
}

func (c *cloudSync) findRemoteMediaFile(ctx context.Context, client *giteeClient, config syncConfigRow, hash string) (string, string, error) {
	repos, err := client.listRepos(ctx)
	if err != nil {
		return "", "", err
	}
	candidates := make([]string, 0, len(repos)+1)
	seen := map[string]bool{}
	add := func(name string) {
		if name != "" && !seen[name] {
			seen[name] = true
			candidates = append(candidates, name)
		}
	}
	add(config.PrimaryRepo)
	for _, repo := range repos {
		if repo.Private && isOfferAtlasMediaName(repo.Name) {
			add(repo.Name)
		}
	}
	paths := []string{"media/" + hash[:2] + "/" + hash, "media/" + hash}
	for _, repoName := range candidates {
		for _, remotePath := range paths {
			contents, _, readErr := client.readFile(ctx, config.Owner, repoName, remotePath)
			if readErr != nil {
				if isGiteeNotFound(readErr) {
					continue
				}
				return "", "", readErr
			}
			actual := sha256.Sum256(contents)
			if hex.EncodeToString(actual[:]) != hash {
				return "", "", fmt.Errorf("remote attachment hash verification failed")
			}
			return repoName, remotePath, nil
		}
	}
	return "", "", errors.New("remote media file was not found in the primary or media repositories")
}

func (c *cloudSync) uploadPending(ctx context.Context, client *giteeClient, config syncConfigRow, cutoffSequence int64) error {
	rows, err := c.store.db.Query(`SELECT id, device_id, sequence, object_type, object_id, action, object_version, base_version, payload, created_at FROM sync_operations WHERE synced_at='' AND sequence<=? ORDER BY sequence`, cutoffSequence)
	if err != nil {
		return err
	}
	operations := make([]syncOperation, 0)
	for rows.Next() {
		var operation syncOperation
		var payload string
		if err := rows.Scan(&operation.ID, &operation.DeviceID, &operation.Sequence, &operation.ObjectType, &operation.ObjectID, &operation.Action, &operation.ObjectVersion, &operation.BaseVersion, &payload, &operation.CreatedAt); err != nil {
			rows.Close()
			return err
		}
		operation.Payload = json.RawMessage(payload)
		operations = append(operations, operation)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(operations) == 0 {
		return nil
	}
	// Keep every local sequence represented by a remote file.  An earlier
	// compaction pass marked an obsolete upsert as synced without uploading it;
	// the operation index then advertised a range containing a hole and every
	// other device failed when it reached that sequence.  Superseded upserts
	// are represented as no-ops below, so deleted attachments are not read while
	// the immutable per-device sequence remains contiguous.
	operations = compactSupersededPendingOperations(operations)
	conflictRows, err := c.store.db.Query(`SELECT object_type, object_id FROM sync_conflicts WHERE status='open'`)
	if err != nil {
		return err
	}
	blocked := map[string]bool{}
	for conflictRows.Next() {
		var objectType, objectID string
		if err := conflictRows.Scan(&objectType, &objectID); err != nil {
			conflictRows.Close()
			return err
		}
		blocked[objectType+":"+objectID] = true
	}
	if err := conflictRows.Close(); err != nil {
		return err
	}
	// Do not skip a blocked sequence: later files are immutable and the
	// operation index must never advertise a gap. The user decision will allow
	// this prefix to continue on the next explicit sync.
	for index, operation := range operations {
		if blocked[operation.ObjectType+":"+operation.ObjectID] {
			operations = operations[:index]
			break
		}
	}
	if len(operations) == 0 {
		return nil
	}
	filesTotal := 0
	for _, operation := range operations {
		if spec, ok := syncSpec(operation.ObjectType); ok && spec.FileKind != "" && operation.Action == "upsert" {
			filesTotal++
		}
	}
	c.setActivityProgress("uploading", len(operations), filesTotal)
	index, indexSHA, err := c.operationIndexForUpload(ctx, client, config, operations[0].Sequence)
	if err != nil {
		return err
	}
	for _, operation := range operations {
		if operation.DeviceID != config.DeviceID {
			return fmt.Errorf("local sync operation %s belongs to another device", operation.ID)
		}
		if operation.Action == "upsert" {
			if err := c.uploadOperationMedia(ctx, client, config, operation); err != nil {
				return err
			}
		}
		data, err := json.Marshal(operation)
		if err != nil {
			return err
		}
		remotePath := fmt.Sprintf("operations/%s/%020d.json", operation.DeviceID, operation.Sequence)
		if err := client.writeFile(ctx, config.Owner, config.PrimaryRepo, remotePath, "Offer Atlas sync operation", data, ""); err != nil {
			// An earlier request may have succeeded while its response was lost. The
			// immutable filename makes it safe to detect and accept that retry.
			if existing, _, readErr := client.readFile(ctx, config.Owner, config.PrimaryRepo, remotePath); readErr == nil && bytesEqual(existing, data) {
				// already present
			} else {
				return err
			}
		}
		if operation.Sequence > index.LatestSequence {
			index.LatestSequence = operation.Sequence
		}
		file := false
		if spec, ok := syncSpec(operation.ObjectType); ok && spec.FileKind != "" && operation.Action == "upsert" {
			file = true
		}
		c.advanceActivityProgress(file)
	}
	if err := c.writeOperationIndex(ctx, client, config, index, indexSHA); err != nil {
		return err
	}
	// Do not mark a batch as synced until both the immutable operations and its
	// discovery index are durable. If an index update fails, retrying safely
	// rewrites the same immutable files and restores the index before local
	// state advances.
	tx, err := c.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, operation := range operations {
		if _, err := tx.Exec(`UPDATE sync_operations SET synced_at=? WHERE id=?`, nowString(), operation.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE sync_object_states SET dirty=0, remote_version=MAX(remote_version, object_version) WHERE object_type=? AND object_id=? AND object_version=?`, operation.ObjectType, operation.ObjectID, operation.ObjectVersion); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// A crash or a quick create-then-delete can leave an object's old upsert and
// its later delete in the local queue together. Uploading that old upsert can
// require an attachment that the user has already removed. Preserve its
// sequence with a no-op operation instead of omitting the file from the remote
// immutable log.
func compactSupersededPendingOperations(operations []syncOperation) []syncOperation {
	lastAction := make(map[string]string, len(operations))
	for _, operation := range operations {
		lastAction[operation.ObjectType+":"+operation.ObjectID] = operation.Action
	}
	result := make([]syncOperation, 0, len(operations))
	for _, operation := range operations {
		key := operation.ObjectType + ":" + operation.ObjectID
		if operation.Action == "upsert" && lastAction[key] == "delete" {
			operation.Action = "noop"
		}
		result = append(result, operation)
	}
	return result
}

func (c *cloudSync) operationIndexForUpload(ctx context.Context, client *giteeClient, config syncConfigRow, firstSequence int64) (remoteOperationIndex, string, error) {
	path := "operations/" + config.DeviceID + "/" + operationIndexName
	contents, sha, err := client.readFile(ctx, config.Owner, config.PrimaryRepo, path)
	if err == nil {
		var index remoteOperationIndex
		if json.Unmarshal(contents, &index) != nil || !index.validFor(config.DeviceID) {
			return remoteOperationIndex{}, "", fmt.Errorf("remote operation index %s has invalid content", path)
		}
		return index, sha, nil
	}
	if !isGiteeNotFound(err) {
		return remoteOperationIndex{}, "", err
	}
	return remoteOperationIndex{Product: "Offer Atlas", Format: syncFormatVersion, DeviceID: config.DeviceID, CanonicalFrom: firstSequence, LatestSequence: firstSequence - 1}, "", nil
}

func (c *cloudSync) writeOperationIndex(ctx context.Context, client *giteeClient, config syncConfigRow, index remoteOperationIndex, sha string) error {
	index.UpdatedAt = nowString()
	contents, err := json.Marshal(index)
	if err != nil {
		return err
	}
	path := "operations/" + config.DeviceID + "/" + operationIndexName
	if err := client.writeFile(ctx, config.Owner, config.PrimaryRepo, path, "Update Offer Atlas operation index", contents, sha); err == nil {
		return nil
	} else if existing, _, readErr := client.readFile(ctx, config.Owner, config.PrimaryRepo, path); readErr == nil && bytesEqual(existing, contents) {
		// A successful write can lose its response. The index is deterministic
		// except for its timestamp, so treat an exact read-back as success.
		return nil
	} else {
		return err
	}
}

func bytesEqual(left, right []byte) bool {
	return len(left) == len(right) && string(left) == string(right)
}

func (c *cloudSync) uploadOperationMedia(ctx context.Context, client *giteeClient, config syncConfigRow, operation syncOperation) error {
	spec, ok := syncSpec(operation.ObjectType)
	if !ok || spec.FileKind == "" {
		return nil
	}
	payload := map[string]any{}
	if err := json.Unmarshal(operation.Payload, &payload); err != nil {
		return err
	}
	hash := stringValue(payload["content_hash"])
	if hash == "" {
		return fmt.Errorf("attachment sync payload is missing content hash")
	}
	var existing int
	if err := c.store.db.QueryRow(`SELECT COUNT(*) FROM sync_media_files WHERE content_hash=?`, hash).Scan(&existing); err != nil {
		return err
	}
	if existing > 0 {
		return nil
	}
	// The primary repository is the authoritative cross-device media index. A
	// different computer may have uploaded this same SHA-256 content first.
	if index, _, indexErr := client.readFile(ctx, config.Owner, config.PrimaryRepo, "media-index/"+hash+".json"); indexErr == nil {
		var entry struct {
			RepoName   string `json:"repoName"`
			RemotePath string `json:"remotePath"`
			SizeBytes  int64  `json:"sizeBytes"`
		}
		if json.Unmarshal(index, &entry) == nil && entry.RepoName != "" && entry.RemotePath != "" {
			_, saveErr := c.store.db.Exec(`INSERT OR IGNORE INTO sync_media_files(content_hash, repo_name, remote_path, size_bytes, updated_at) VALUES (?, ?, ?, ?, ?)`, hash, entry.RepoName, entry.RemotePath, entry.SizeBytes, nowString())
			return saveErr
		}
	}
	filePath, err := c.filePathForPayload(spec, payload)
	if err != nil {
		return err
	}
	contents, err := os.ReadFile(filePath)
	if errors.Is(err, os.ErrNotExist) {
		// The operation may refer to a file whose local copy was lost after its
		// metadata was captured. A previously uploaded media object can heal it
		// by hash; do this only for the operation that is actually being sent.
		if restoreErr := c.materializeRemoteFile(ctx, client, config, operation); restoreErr == nil {
			filePath, err = c.filePathForPayload(spec, payload)
			if err == nil {
				contents, err = os.ReadFile(filePath)
			}
		} else {
			return fmt.Errorf("attachment file is missing locally and could not be restored: %w", restoreErr)
		}
	}
	if err != nil {
		return fmt.Errorf("read attachment for sync: %w", err)
	}
	actual := sha256.Sum256(contents)
	if hex.EncodeToString(actual[:]) != hash {
		return fmt.Errorf("attachment changed while preparing sync")
	}
	repo, err := c.selectMediaRepo(ctx, client, config.Owner, int64(len(contents)))
	if err != nil {
		return err
	}
	remotePath := "media/" + hash[:2] + "/" + hash
	if err := client.writeFile(ctx, config.Owner, repo.Name, remotePath, "Store Offer Atlas attachment", contents, ""); err != nil {
		if _, _, checkErr := client.readFile(ctx, config.Owner, repo.Name, remotePath); checkErr != nil {
			return err
		}
	}
	index, _ := json.Marshal(map[string]any{"contentHash": hash, "repoName": repo.Name, "remotePath": remotePath, "sizeBytes": len(contents)})
	indexPath := "media-index/" + hash + ".json"
	if err := client.writeFile(ctx, config.Owner, config.PrimaryRepo, indexPath, "Index Offer Atlas attachment", index, ""); err != nil {
		if existing, _, readErr := client.readFile(ctx, config.Owner, config.PrimaryRepo, indexPath); readErr != nil {
			return err
		} else {
			var indexed struct {
				RepoName   string `json:"repoName"`
				RemotePath string `json:"remotePath"`
				SizeBytes  int64  `json:"sizeBytes"`
			}
			if json.Unmarshal(existing, &indexed) != nil || indexed.RepoName == "" || indexed.RemotePath == "" {
				return err
			}
			repo.Name, remotePath = indexed.RepoName, indexed.RemotePath
		}
	}
	_, err = c.store.db.Exec(`INSERT OR IGNORE INTO sync_media_files(content_hash, repo_name, remote_path, size_bytes, updated_at) VALUES (?, ?, ?, ?, ?)`, hash, repo.Name, remotePath, len(contents), nowString())
	return err
}

func (c *cloudSync) selectMediaRepo(ctx context.Context, client *giteeClient, owner string, incoming int64) (giteeRepo, error) {
	repos, err := client.listRepos(ctx)
	if err != nil {
		return giteeRepo{}, err
	}
	type candidate struct {
		repo giteeRepo
		used int64
	}
	items := []candidate{}
	for _, repo := range repos {
		if !repo.Private || !isOfferAtlasMediaName(repo.Name) {
			continue
		}
		if !c.repoHasMarker(ctx, client, owner, repo.Name, mediaFormatPath, "media") {
			continue
		}
		var used int64
		if err := c.store.db.QueryRow(`SELECT COALESCE(SUM(size_bytes), 0) FROM sync_media_files WHERE repo_name=?`, repo.Name).Scan(&used); err != nil {
			return giteeRepo{}, err
		}
		items = append(items, candidate{repo, used})
	}
	provisioning, err := c.provisioningRepos(owner, "media")
	if err != nil {
		return giteeRepo{}, err
	}
	for _, repoName := range provisioning {
		var repo giteeRepo
		found := false
		for _, listed := range repos {
			if strings.EqualFold(listed.Name, repoName) {
				repo, found = listed, true
				break
			}
		}
		if !found {
			if err := c.forgetProvisioningRepo(owner, repoName); err != nil {
				return giteeRepo{}, err
			}
			continue
		}
		if !repo.Private || !isOfferAtlasMediaName(repo.Name) {
			return giteeRepo{}, fmt.Errorf("等待初始化的附件仓库 %q 不符合 Offer Atlas 专用仓库规则", repo.Name)
		}
		if err := c.ensureRepoMarker(ctx, client, owner, repo.Name, mediaFormatPath, "media"); err != nil {
			return giteeRepo{}, fmt.Errorf("恢复 Gitee 附件仓库初始化: %w", err)
		}
		if err := c.forgetProvisioningRepo(owner, repo.Name); err != nil {
			return giteeRepo{}, err
		}
		return repo, nil
	}
	sort.Slice(items, func(i, j int) bool { return items[i].repo.Name > items[j].repo.Name })
	for _, item := range items {
		if item.used+incoming <= mediaSoftLimit {
			return item.repo, nil
		}
	}
	for number := 1; ; number++ {
		name := fmt.Sprintf("offer-atlas-media-%03d", number)
		exists := false
		for _, repo := range repos {
			if strings.EqualFold(repo.Name, name) {
				exists = true
				break
			}
		}
		if exists {
			continue
		}
		repo, err := client.createPrivateRepo(ctx, name, "Offer Atlas attachment media storage")
		if err != nil {
			return giteeRepo{}, fmt.Errorf("创建 Gitee 附件仓库: %w", err)
		}
		if err := c.rememberProvisioningRepo(owner, repo.Name, "media"); err != nil {
			return giteeRepo{}, fmt.Errorf("记录 Gitee 附件仓库初始化状态: %w", err)
		}
		if err := c.ensureRepoMarker(ctx, client, owner, repo.Name, mediaFormatPath, "media"); err != nil {
			return giteeRepo{}, err
		}
		if err := c.forgetProvisioningRepo(owner, repo.Name); err != nil {
			return giteeRepo{}, err
		}
		return repo, nil
	}
}

func (c *cloudSync) listConflicts() ([]domain.SyncConflict, error) {
	rows, err := c.store.db.Query(`SELECT id, object_type, object_id, local_payload, remote_payload, local_updated_at, remote_updated_at, remote_action FROM sync_conflicts WHERE status='open' ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.SyncConflict{}
	for rows.Next() {
		var item domain.SyncConflict
		var localPayload, remotePayload string
		var remoteAction string
		if err := rows.Scan(&item.ID, &item.ObjectType, &item.ObjectID, &localPayload, &remotePayload, &item.LocalUpdatedAt, &item.RemoteUpdatedAt, &remoteAction); err != nil {
			return nil, err
		}
		item.LocalDescription = describeSyncPayload(item.ObjectType, localPayload)
		if remoteAction == "delete" {
			item.RemoteDescription = "云端已删除此记录"
		} else {
			item.RemoteDescription = describeSyncPayload(item.ObjectType, remotePayload)
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func describeSyncPayload(objectType, payload string) string {
	var value map[string]any
	if json.Unmarshal([]byte(payload), &value) != nil {
		return objectType
	}
	fields := []string{"name", "title", "content", "original_name", "job_code"}
	for _, field := range fields {
		if displayed := strings.TrimSpace(stringValue(value[field])); displayed != "" {
			return displayed
		}
	}
	return objectType + " · " + stringValue(value["id"])
}

func (c *cloudSync) resolveConflict(ctx context.Context, id, choice string) error {
	if choice != "local" && choice != "remote" {
		return errors.New("冲突处理方式无效")
	}
	var objectType, objectID, localPayload, remotePayload, remoteOperationID, remoteAction string
	var remoteVersion int64
	err := c.store.db.QueryRow(`SELECT object_type, object_id, local_payload, remote_payload, remote_operation_id, remote_action, remote_version FROM sync_conflicts WHERE id=? AND status='open'`, id).Scan(&objectType, &objectID, &localPayload, &remotePayload, &remoteOperationID, &remoteAction, &remoteVersion)
	if err != nil {
		return err
	}
	if choice == "remote" {
		if err := c.withImporting(func() error {
			config, configErr := c.config()
			if configErr != nil {
				return configErr
			}
			if remoteAction == "upsert" {
				token, tokenErr := c.readToken()
				if tokenErr != nil {
					return tokenErr
				}
				remote := syncOperation{ObjectType: objectType, ObjectID: objectID, Action: remoteAction, Payload: json.RawMessage(remotePayload)}
				if err := c.materializeRemoteFile(ctx, c.client(token), config, remote); err != nil {
					return err
				}
			}
			tx, err := c.store.db.BeginTx(ctx, nil)
			if err != nil {
				return err
			}
			defer tx.Rollback()
			if remoteAction == "delete" {
				if err := deleteSyncedObject(tx, objectType, objectID); err != nil {
					return err
				}
			} else {
				if err := upsertSyncedObject(tx, objectType, json.RawMessage(remotePayload)); err != nil {
					return err
				}
			}
			// The discarded local branch still owns immutable device sequences.
			// Convert those pending records to no-ops instead of marking them
			// synced, otherwise the next upload would advertise a sequence gap.
			if _, err := tx.Exec(`UPDATE sync_operations SET action='noop', payload='{}', synced_at='' WHERE object_type=? AND object_id=? AND synced_at=''`, objectType, objectID); err != nil {
				return err
			}
			hash := ""
			if remoteAction == "upsert" {
				hash = contentHash([]byte(remotePayload))
			}
			if _, err := tx.Exec(`UPDATE sync_object_states SET object_version=MAX(object_version, ?), remote_version=?, dirty=0, deleted=?, content_hash=?, updated_at=? WHERE object_type=? AND object_id=?`, remoteVersion, remoteVersion, boolToInt(remoteAction == "delete"), hash, nowString(), objectType, objectID); err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE sync_conflicts SET status='resolved_remote', resolved_at=? WHERE id=?`, nowString(), id); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT OR IGNORE INTO sync_applied_operations(operation_id, applied_at) VALUES (?, ?)`, remoteOperationID, nowString()); err != nil {
				return err
			}
			if err := tx.Commit(); err != nil {
				return err
			}
			if remoteAction == "delete" {
				return c.cleanupDeletedRemoteFile(objectType, json.RawMessage(remotePayload))
			}
			return nil
		}); err != nil {
			return err
		}
		c.scheduleLocalSync(0)
		return nil
	}
	config, err := c.config()
	if err != nil {
		return err
	}
	states, stateErr := c.objectStates()
	if stateErr != nil {
		return stateErr
	}
	state, hasState := states[objectType+":"+objectID]
	if !hasState {
		return errors.New("本机冲突记录已不存在，无法保留")
	}
	action := "upsert"
	payload := json.RawMessage(localPayload)
	if state.Deleted {
		action = "delete"
	} else if snapshot, snapshotErr := c.snapshotFor(objectType, objectID); snapshotErr == nil {
		payload = snapshot.Payload
	} else {
		return snapshotErr
	}
	if err := c.withImporting(func() error {
		tx, txErr := c.store.db.BeginTx(ctx, nil)
		if txErr != nil {
			return txErr
		}
		defer tx.Rollback()
		version := state.Version + 1
		if remoteVersion >= version {
			version = remoteVersion + 1
		}
		// Preserve every abandoned local sequence as a no-op. The chosen local
		// snapshot is represented by the new operation inserted below.
		if _, err := tx.Exec(`UPDATE sync_operations SET action='noop', payload='{}', synced_at='' WHERE object_type=? AND object_id=? AND synced_at=''`, objectType, objectID); err != nil {
			return err
		}
		op := syncOperation{ID: uuid.NewString(), DeviceID: config.DeviceID, Sequence: config.NextSequence, ObjectType: objectType, ObjectID: objectID, Action: action, ObjectVersion: version, BaseVersion: remoteVersion, Payload: payload, CreatedAt: nowString()}
		if err := insertSyncOperation(tx, op); err != nil {
			return err
		}
		hash := ""
		if action == "upsert" {
			hash = contentHash(payload)
		}
		if _, err := tx.Exec(`UPDATE sync_object_states SET object_version=?, remote_version=?, dirty=1, deleted=?, content_hash=?, updated_at=? WHERE object_type=? AND object_id=?`, version, remoteVersion, boolToInt(action == "delete"), hash, nowString(), objectType, objectID); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE sync_config SET next_sequence=next_sequence+1, updated_at=? WHERE id=1`, nowString()); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE sync_conflicts SET status='resolved_local', resolved_at=? WHERE id=?`, nowString(), id); err != nil {
			return err
		}
		return tx.Commit()
	}); err != nil {
		return err
	}
	c.scheduleLocalSync(0)
	return nil
}
