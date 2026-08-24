package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	stdruntime "runtime"
	"strings"
	"sync"
	"time"

	"github.com/dingyu/offer-atlas/internal/domain"
	"github.com/dingyu/offer-atlas/internal/store"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// App exposes the application use cases to the Wails frontend.
type App struct {
	ctx           context.Context
	store         *store.Store
	updater       *updateManager
	closeMu       sync.Mutex
	allowClose    bool
	closing       bool
	pendingUpdate *updateInstallPlan
}

const maxUploadedAttachmentBytes = 25 * 1024 * 1024

func NewApp() *App {
	return &App{}
}

func (a *App) Startup(ctx context.Context) {
	a.ctx = ctx
	dataPath, err := defaultDatabasePath()
	if err != nil {
		panic(fmt.Errorf("resolve application data path: %w", err))
	}

	database, err := store.Open(dataPath)
	if err != nil {
		panic(fmt.Errorf("open application database: %w", err))
	}
	a.store = database
	updater, err := newUpdateManager(filepath.Dir(dataPath))
	if err != nil {
		panic(fmt.Errorf("start update manager: %w", err))
	}
	a.updater = updater
	go func() {
		// Let the primary workspace render first. An automatic update check never
		// interrupts work; it simply makes the header entry available when needed.
		timer := time.NewTimer(4 * time.Second)
		defer timer.Stop()
		select {
		case <-timer.C:
			_, _ = a.updater.check(context.Background(), false)
		case <-ctx.Done():
			return
		}
		periodic := time.NewTicker(updateCheckInterval)
		defer periodic.Stop()
		for {
			select {
			case <-periodic.C:
				_, _ = a.updater.check(context.Background(), false)
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (a *App) Shutdown(context.Context) {
	if a.store != nil {
		_ = a.store.Close()
	}
}

// BeforeClose keeps the application open while a connected cloud workspace has
// unsynced work. It intentionally offers no bypass: local data is already
// saved, then the app exits automatically only after Gitee catches up.
func (a *App) BeforeClose(ctx context.Context) bool {
	a.closeMu.Lock()
	installingUpdate := a.pendingUpdate != nil
	needsSync := a.store != nil && a.store.NeedsSyncBeforeClose()
	if a.allowClose || (!installingUpdate && (a.store == nil || !needsSync)) {
		a.closeMu.Unlock()
		return false
	}
	if a.closing {
		a.closeMu.Unlock()
		return true
	}
	a.closing = true
	a.closeMu.Unlock()
	if installingUpdate {
		wailsruntime.EventsEmit(ctx, "app-update:closing", "新版本已准备完成，正在确认云端同步状态")
	} else {
		wailsruntime.EventsEmit(ctx, "cloud-sync:closing", "正在确认云端状态")
	}
	go func() {
		// An update always runs this guarded check. A user could have made one
		// final edit between clicking the update button and Wails requesting the
		// close, so the preflight NeedsSyncBeforeClose result alone is not enough.
		// Ordinary window closes retain their existing no-extra-work behaviour.
		if needsSync || installingUpdate {
			if err := a.store.SyncBeforeClose(context.Background()); err != nil {
				a.handleCloseFailure(ctx, installingUpdate, err)
				return
			}
		}
		if installingUpdate {
			a.closeMu.Lock()
			plan := a.pendingUpdate
			a.closeMu.Unlock()
			if plan == nil || a.updater == nil {
				a.handleCloseFailure(ctx, true, fmt.Errorf("更新安装计划已丢失，请重新下载更新"))
				return
			}
			wailsruntime.EventsEmit(ctx, "app-update:closing", "云端状态已确认，正在安全启动新版本")
			if err := a.updater.launchInstaller(*plan); err != nil {
				a.updater.cancelInstall(err)
				a.handleCloseFailure(ctx, true, err)
				return
			}
		}
		a.closeMu.Lock()
		a.allowClose = true
		a.closing = false
		a.closeMu.Unlock()
		wailsruntime.Quit(ctx)
	}()
	return true
}

func (a *App) handleCloseFailure(ctx context.Context, installingUpdate bool, err error) {
	if installingUpdate {
		if a.updater != nil {
			a.updater.cancelInstall(err)
		}
		wailsruntime.EventsEmit(ctx, "app-update:install-failed", "更新未安装，应用保持打开："+err.Error())
	} else {
		wailsruntime.EventsEmit(ctx, "cloud-sync:close-failed", "同步未完成，应用保持打开："+err.Error())
	}
	a.closeMu.Lock()
	a.closing = false
	if installingUpdate {
		a.pendingUpdate = nil
	}
	a.closeMu.Unlock()
}

// AppUpdateStatus returns the latest local update-check snapshot. It never
// makes a network request, so the frontend can poll it while a package is
// downloading to render accurate progress.
func (a *App) AppUpdateStatus() (AppUpdate, error) {
	if a.updater == nil {
		return AppUpdate{}, fmt.Errorf("更新服务正在启动")
	}
	return a.updater.snapshot(), nil
}

func (a *App) CheckForAppUpdate() (AppUpdate, error) {
	if a.updater == nil {
		return AppUpdate{}, fmt.Errorf("更新服务正在启动")
	}
	return a.updater.check(a.ctx, true)
}

func (a *App) DownloadAppUpdate() (AppUpdate, error) {
	if a.updater == nil {
		return AppUpdate{}, fmt.Errorf("更新服务正在启动")
	}
	return a.updater.download(a.ctx)
}

// InstallDownloadedUpdate begins the same guarded exit path used by the
// window close button. The helper starts only after unsynced Gitee changes are
// confirmed, then waits for this process to exit before replacing the exe.
func (a *App) InstallDownloadedUpdate() error {
	if a.updater == nil {
		return fmt.Errorf("更新服务正在启动")
	}
	plan, err := a.updater.prepareInstall()
	if err != nil {
		return err
	}
	a.closeMu.Lock()
	if a.closing || a.pendingUpdate != nil {
		a.closeMu.Unlock()
		a.updater.cancelInstall(fmt.Errorf("应用正在退出，请稍候"))
		return fmt.Errorf("应用正在退出，请稍候")
	}
	a.pendingUpdate = &plan
	a.closeMu.Unlock()
	wailsruntime.Quit(a.ctx)
	return nil
}

func (a *App) Health() (domain.Health, error) {
	if a.store == nil {
		return domain.Health{}, fmt.Errorf("application is still starting")
	}
	return a.store.Health()
}

func (a *App) Dashboard() (domain.Dashboard, error) {
	return a.store.Dashboard(time.Now().UTC())
}

func (a *App) ListCompanies() ([]domain.Company, error) {
	return a.store.ListCompanies()
}

func (a *App) SaveCompany(input domain.CompanyInput) (domain.Company, error) {
	return a.store.SaveCompany(input)
}

func (a *App) ListCampaigns(companyID string) ([]domain.Campaign, error) {
	return a.store.ListCampaigns(companyID)
}

func (a *App) SaveCampaign(input domain.CampaignInput) (domain.Campaign, error) {
	return a.store.SaveCampaign(input)
}

func (a *App) ListPositions(filter domain.PositionFilter) (domain.PositionPage, error) {
	return a.store.ListPositionPage(filter)
}

func (a *App) ListApplications(filter domain.ApplicationFilter) (domain.ApplicationPage, error) {
	return a.store.ListApplications(filter)
}

func (a *App) SavePosition(input domain.PositionInput) (domain.Position, error) {
	return a.store.SavePosition(input)
}

func (a *App) QuickCapturePosition(input domain.QuickCapturePositionInput) (domain.Position, error) {
	return a.store.QuickCapturePosition(input)
}

func (a *App) PreviewDeletion(input domain.DeleteInput) (domain.DeletionPreview, error) {
	return a.store.PreviewDeletion(input)
}

func (a *App) DeleteEntity(input domain.DeleteInput) error {
	return a.store.DeleteEntity(input)
}

func (a *App) SaveApplication(input domain.ApplicationInput) (domain.Application, error) {
	return a.store.SaveApplication(input)
}

func (a *App) GetPositionDetail(positionID string) (domain.PositionDetail, error) {
	return a.store.GetPositionDetail(positionID)
}

func (a *App) GetApplicationDetail(applicationID string) (domain.ApplicationDetail, error) {
	return a.store.GetApplicationDetail(applicationID)
}

func (a *App) ListApplicationStages(applicationID string) ([]domain.ApplicationStage, error) {
	return a.store.ListApplicationStages(applicationID)
}

func (a *App) ListScheduleItems(filter domain.ScheduleFilter) ([]domain.ScheduleItem, error) {
	return a.store.ListScheduleItems(filter)
}

func (a *App) ListStageTypes() ([]domain.StageTypeDefinition, error) {
	return a.store.ListStageTypes()
}

func (a *App) SaveStageType(input domain.StageTypeInput) (domain.StageTypeDefinition, error) {
	return a.store.SaveStageType(input)
}

func (a *App) DeleteStageType(id domain.StageType) error {
	return a.store.DeleteStageType(id)
}

func (a *App) SaveApplicationStage(input domain.ApplicationStageInput) (domain.ApplicationStage, error) {
	return a.store.SaveApplicationStage(input)
}

func (a *App) DeleteApplicationStage(stageID string) error {
	return a.store.DeleteApplicationStage(stageID)
}

func (a *App) ReorderApplicationStages(applicationID string, stageIDs []string) ([]domain.ApplicationStage, error) {
	return a.store.ReorderApplicationStages(applicationID, stageIDs)
}

// AddPositionAttachments opens the native file picker and imports copies into
// OfferAtlas-managed storage, so the original local files can safely move.
func (a *App) AddPositionAttachments(positionID string) ([]domain.PositionAttachment, error) {
	paths, err := wailsruntime.OpenMultipleFilesDialog(a.ctx, wailsruntime.OpenDialogOptions{
		Title: "添加岗位附件",
		Filters: []wailsruntime.FileFilter{
			{DisplayName: "图片和常用文档", Pattern: "*.png;*.jpg;*.jpeg;*.webp;*.gif;*.pdf;*.doc;*.docx;*.txt;*.md"},
			{DisplayName: "所有文件", Pattern: "*.*"},
		},
	})
	if err != nil {
		return nil, err
	}
	return a.store.ImportPositionAttachments(positionID, paths)
}

func (a *App) ListPositionAttachments(positionID string) ([]domain.PositionAttachment, error) {
	return a.store.ListPositionAttachments(positionID)
}

func (a *App) PastePositionImage(positionID, originalName, dataURL string) ([]domain.PositionAttachment, error) {
	mimeType, contents, err := attachmentDataURL(dataURL)
	if err != nil {
		return nil, fmt.Errorf("decode clipboard image: %w", err)
	}
	if !isPastedImageMIMEType(mimeType) {
		return nil, fmt.Errorf("clipboard data is not a supported image")
	}
	return a.store.ImportPastedPositionImage(positionID, originalName, mimeType, contents)
}

// UploadPositionAttachment imports a browser-selected file that was staged in
// the quick-capture form. Files are copied into managed storage like files
// chosen from the native attachment picker.
func (a *App) UploadPositionAttachment(positionID, originalName, dataURL string) ([]domain.PositionAttachment, error) {
	mimeType, contents, err := attachmentDataURL(dataURL)
	if err != nil {
		return nil, fmt.Errorf("decode attachment: %w", err)
	}
	return a.store.ImportPositionAttachmentData(positionID, originalName, mimeType, contents)
}

// UploadApplicationResume stores a managed copy of the exact document used
// for one application. A later upload safely replaces the prior copy.
func (a *App) UploadApplicationResume(applicationID, originalName, dataURL string) (domain.ApplicationResume, error) {
	mimeType, contents, err := attachmentDataURL(dataURL)
	if err != nil {
		return domain.ApplicationResume{}, fmt.Errorf("decode resume: %w", err)
	}
	return a.store.ImportApplicationResumeData(applicationID, originalName, mimeType, contents)
}

func (a *App) ListResumes(includeArchived bool) ([]domain.Resume, error) {
	return a.store.ListResumes(includeArchived)
}

func (a *App) UploadResume(name, originalName, dataURL string) (domain.Resume, error) {
	mimeType, contents, err := attachmentDataURL(dataURL)
	if err != nil {
		return domain.Resume{}, fmt.Errorf("decode resume: %w", err)
	}
	return a.store.ImportResumeData(name, originalName, mimeType, contents)
}

func (a *App) SaveResume(input domain.ResumeInput) (domain.Resume, error) {
	return a.store.SaveResume(input)
}

func (a *App) OpenResume(id string) error {
	path, err := a.store.ResumePath(id)
	if err != nil {
		return err
	}
	if stdruntime.GOOS != "windows" {
		return fmt.Errorf("opening resumes is currently supported on Windows only")
	}
	return exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", path).Start()
}

func (a *App) DeleteResume(id string) error {
	return a.store.DeleteResume(id)
}

func (a *App) SetApplicationResume(applicationID, resumeID string) error {
	return a.store.SetApplicationResume(applicationID, resumeID)
}

func (a *App) ClearApplicationResume(applicationID string) error {
	return a.store.ClearApplicationResume(applicationID)
}

func attachmentDataURL(dataURL string) (string, []byte, error) {
	metadata, encoded, found := strings.Cut(strings.TrimSpace(dataURL), ",")
	metadataLower := strings.ToLower(metadata)
	if !found || !strings.HasPrefix(metadataLower, "data:") || !strings.HasSuffix(metadataLower, ";base64") {
		return "", nil, fmt.Errorf("invalid data URL")
	}
	mimeType := strings.TrimSpace(metadata[len("data:") : len(metadata)-len(";base64")])
	if separator := strings.IndexByte(mimeType, ';'); separator >= 0 {
		mimeType = strings.TrimSpace(mimeType[:separator])
	}
	if mimeType == "" || len(mimeType) > 127 {
		return "", nil, fmt.Errorf("invalid attachment MIME type")
	}
	contents, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", nil, err
	}
	if len(contents) == 0 {
		return "", nil, fmt.Errorf("attachment is empty")
	}
	if len(contents) > maxUploadedAttachmentBytes {
		return "", nil, fmt.Errorf("attachment exceeds the %d MB limit", maxUploadedAttachmentBytes/(1024*1024))
	}
	return strings.ToLower(mimeType), contents, nil
}

func isPastedImageMIMEType(mimeType string) bool {
	switch strings.ToLower(mimeType) {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/bmp":
		return true
	default:
		return false
	}
}

func (a *App) PositionAttachmentDataURL(id string) (string, error) {
	return a.store.PositionAttachmentDataURL(id)
}

func (a *App) OpenPositionAttachment(id string) error {
	path, err := a.store.PositionAttachmentPath(id)
	if err != nil {
		return err
	}
	if stdruntime.GOOS != "windows" {
		return fmt.Errorf("opening attachments is currently supported on Windows only")
	}
	return exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", path).Start()
}

func (a *App) OpenApplicationResume(applicationID string) error {
	path, err := a.store.ApplicationResumePath(applicationID)
	if err != nil {
		return err
	}
	if stdruntime.GOOS != "windows" {
		return fmt.Errorf("opening resumes is currently supported on Windows only")
	}
	return exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", path).Start()
}

func (a *App) DeleteApplicationResume(applicationID string) error {
	return a.store.DeleteApplicationResume(applicationID)
}

func (a *App) DeletePositionAttachment(id string) error {
	return a.store.DeletePositionAttachment(id)
}

func (a *App) CreateBackup() (string, error) {
	return a.store.CreateBackup()
}

func (a *App) BackupCenter() (domain.BackupCenter, error) {
	return a.store.BackupCenter()
}

func (a *App) CloudSyncStatus() (domain.CloudSyncStatus, error) {
	return a.store.CloudSyncStatus()
}

func (a *App) ConnectGitee(token string) (domain.GiteeConnectionPreview, error) {
	return a.store.ConnectGitee(a.ctx, token)
}

func (a *App) PendingGiteeConnectionPreview() (domain.GiteeConnectionPreview, error) {
	return a.store.PendingGiteeConnectionPreview(a.ctx)
}

func (a *App) ConfirmGiteeConnection(mode string) (domain.CloudSyncStatus, error) {
	return a.store.ConfirmGiteeConnection(a.ctx, mode)
}

func (a *App) SyncGiteeNow() (domain.CloudSyncStatus, error) {
	return a.store.SyncGiteeNow(a.ctx)
}

func (a *App) DisconnectGitee() error {
	return a.store.DisconnectGitee()
}

func (a *App) DeleteGiteeSyncRepositories() ([]string, error) {
	return a.store.DeleteGiteeSyncRepositories(a.ctx)
}

func (a *App) ListSyncConflicts() ([]domain.SyncConflict, error) {
	return a.store.ListSyncConflicts()
}

func (a *App) ResolveSyncConflict(id, choice string) error {
	return a.store.ResolveSyncConflict(a.ctx, id, choice)
}

func (a *App) RestoreBackup(id, confirmation string) (domain.RestoreResult, error) {
	return a.store.RestoreBackup(id, confirmation)
}

func (a *App) OpenBackupLocation(kind string) error {
	if stdruntime.GOOS != "windows" {
		return fmt.Errorf("opening backup folders is currently supported on Windows only")
	}
	center, err := a.store.BackupCenter()
	if err != nil {
		return err
	}
	var path string
	switch kind {
	case "data":
		path = center.DataDirectory
	case "backups":
		path = center.BackupDirectory
	case "mirror":
		path = center.MirrorDirectory
	default:
		return fmt.Errorf("unknown backup location %q", kind)
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return err
	}
	return exec.Command("explorer.exe", path).Start()
}

func defaultDatabasePath() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	directory := filepath.Join(configDir, "OfferAtlas")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(directory, "offer-atlas.db"), nil
}
