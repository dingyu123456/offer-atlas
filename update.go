package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultUpdateAPIURL      = "https://api.github.com/repos/dingyu123456/offer-atlas/releases/latest"
	defaultUpdateAssetAPIURL = "https://api.github.com/repos/dingyu123456/offer-atlas/releases/assets/"
	// A launch always checks immediately. This interval is only for long-running
	// sessions, so it stays comfortably below the old once-per-day cadence
	// without approaching GitHub's unauthenticated API rate limit.
	updateCheckInterval   = 6 * time.Hour
	updateCheckTimeout    = 5 * time.Second
	updateCheckRetryCount = 5
	updateCheckRetryDelay = time.Second
	updateDownloadTimeout = 15 * time.Minute
)

// buildVersion is intentionally a variable so release builds can override it
// with -ldflags. The checked-in value is also the version shown in development
// builds and in the update dialog.
var buildVersion = "0.2.3"

// AppUpdate is a user-facing snapshot. It contains no credential or business
// data and can safely be exposed to the Wails frontend.
type AppUpdate struct {
	CurrentVersion  string `json:"currentVersion"`
	LatestVersion   string `json:"latestVersion"`
	Available       bool   `json:"available"`
	State           string `json:"state"`
	ReleaseNotes    string `json:"releaseNotes"`
	PublishedAt     string `json:"publishedAt"`
	ReleaseURL      string `json:"releaseUrl"`
	AssetName       string `json:"assetName"`
	AssetSize       int64  `json:"assetSize"`
	DownloadedBytes int64  `json:"downloadedBytes"`
	Message         string `json:"message"`
	CheckedAt       string `json:"checkedAt"`
}

type githubRelease struct {
	TagName     string               `json:"tag_name"`
	Body        string               `json:"body"`
	HTMLURL     string               `json:"html_url"`
	PublishedAt string               `json:"published_at"`
	Assets      []githubReleaseAsset `json:"assets"`
}

type githubReleaseAsset struct {
	ID                 int64  `json:"id"`
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
	Digest             string `json:"digest"`
}

type updateCheckHTTPError struct {
	status int
}

func (e *updateCheckHTTPError) Error() string {
	return fmt.Sprintf("GitHub 返回 HTTP %d", e.status)
}

type updateCheckRetriesExhaustedError struct {
	last error
}

func (e *updateCheckRetriesExhaustedError) Error() string {
	return fmt.Sprintf("更新检查连续失败：%v", e.last)
}

func (e *updateCheckRetriesExhaustedError) Unwrap() error {
	return e.last
}

type updatePersistentState struct {
	CheckedAt string `json:"checkedAt"`
}

type updateInstallPlan struct {
	downloadPath string
	targetPath   string
}

// updateManager owns the release metadata and the verified downloaded package.
// It deliberately has no dependency on the data store: replacing the program
// must never move, overwrite, or restore user data.
type updateManager struct {
	mu             sync.RWMutex
	status         AppUpdate
	assetURL       string
	assetDigest    string
	downloadPath   string
	apiURL         string
	client         *http.Client
	stagingDir     string
	statePath      string
	executablePath string
	launch         func(string, ...string) error
}

func newUpdateManager(dataDirectory string) (*updateManager, error) {
	stagingDir := filepath.Join(dataDirectory, "updates")
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		return nil, fmt.Errorf("create update staging directory: %w", err)
	}
	executablePath, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate running application: %w", err)
	}
	manager := &updateManager{
		status: AppUpdate{
			CurrentVersion: normalizeVersion(buildVersion),
			State:          "idle",
			Message:        "可检查应用更新",
		},
		apiURL: defaultUpdateAPIURL,
		// Time limits belong to the request contexts below. A single Client timeout
		// incorrectly applies the short metadata deadline to the entire package
		// download, including a slow but otherwise healthy response body.
		client:         &http.Client{},
		stagingDir:     stagingDir,
		statePath:      filepath.Join(stagingDir, "state.json"),
		executablePath: executablePath,
		launch: func(name string, args ...string) error {
			return exec.Command(name, args...).Start()
		},
	}
	manager.loadPersistentState()
	return manager, nil
}

func (m *updateManager) loadPersistentState() {
	contents, err := os.ReadFile(m.statePath)
	if err != nil {
		return
	}
	var saved updatePersistentState
	if json.Unmarshal(contents, &saved) == nil {
		m.status.CheckedAt = saved.CheckedAt
	}
}

func (m *updateManager) persistCheckedAt() {
	m.mu.RLock()
	contents, err := json.Marshal(updatePersistentState{CheckedAt: m.status.CheckedAt})
	m.mu.RUnlock()
	if err != nil {
		return
	}
	temporary, err := os.CreateTemp(m.stagingDir, "state-*.json")
	if err != nil {
		return
	}
	name := temporary.Name()
	if _, err = temporary.Write(contents); err == nil {
		err = temporary.Close()
	} else {
		_ = temporary.Close()
	}
	if err == nil {
		// Windows does not allow Rename to replace an existing destination. This
		// tiny cache is not business data, so replacing it after the complete
		// temporary file is closed is sufficient and keeps daily checks stable.
		_ = os.Remove(m.statePath)
		_ = os.Rename(name, m.statePath)
		return
	}
	_ = os.Remove(name)
}

func (m *updateManager) snapshot() AppUpdate {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.status
}

func (m *updateManager) shouldAutoCheck(now time.Time) bool {
	m.mu.RLock()
	checkedAt := m.status.CheckedAt
	state := m.status.State
	m.mu.RUnlock()
	if state == "checking" || state == "downloading" || state == "installing" {
		return false
	}
	if checkedAt == "" {
		return true
	}
	parsed, err := time.Parse(time.RFC3339Nano, checkedAt)
	return err != nil || now.Sub(parsed) >= updateCheckInterval
}

// check returns whether a release newer than the running application exists.
// Startup and user-initiated checks are forced. Background checks retain a
// small persistent cache so a long-running app does not poll GitHub needlessly.
func (m *updateManager) check(ctx context.Context, manual bool, force bool) (AppUpdate, error) {
	if !force && !manual && !m.shouldAutoCheck(time.Now().UTC()) {
		return m.snapshot(), nil
	}
	m.mu.Lock()
	if m.status.State == "checking" || m.status.State == "downloading" || m.status.State == "installing" {
		status := m.status
		m.mu.Unlock()
		return status, nil
	}
	m.status.State = "checking"
	m.status.Message = updateCheckAttemptMessage(1)
	m.status.DownloadedBytes = 0
	m.mu.Unlock()

	release, err := m.fetchLatestRelease(ctx)
	if err != nil {
		return m.finishCheckError(manual, err)
	}
	latestVersion := normalizeVersion(release.TagName)
	if latestVersion == "" {
		return m.finishCheckError(manual, errors.New("GitHub Release 未包含有效版本号"))
	}
	asset, ok := selectUpdateAsset(release.Assets)
	m.mu.RLock()
	currentVersion := normalizeVersion(m.status.CurrentVersion)
	m.mu.RUnlock()
	if currentVersion == "" {
		currentVersion = normalizeVersion(buildVersion)
	}
	if versionGreater(latestVersion, currentVersion) && !ok {
		return m.finishCheckError(manual, errors.New("新版本未提供 Windows 安装包"))
	}

	checkedAt := time.Now().UTC().Format(time.RFC3339Nano)
	m.mu.Lock()
	m.status.CheckedAt = checkedAt
	m.status.LatestVersion = latestVersion
	m.status.ReleaseNotes = strings.TrimSpace(release.Body)
	m.status.PublishedAt = release.PublishedAt
	m.status.ReleaseURL = release.HTMLURL
	m.status.Available = versionGreater(latestVersion, currentVersion)
	m.downloadPath = ""
	m.assetURL = ""
	m.assetDigest = ""
	m.status.DownloadedBytes = 0
	if m.status.Available {
		m.assetURL = downloadURLForAsset(asset)
		m.assetDigest = asset.Digest
		m.status.AssetName = asset.Name
		m.status.AssetSize = asset.Size
		m.status.State = "available"
		m.status.Message = "发现可用新版本"
	} else {
		m.status.AssetName = ""
		m.status.AssetSize = 0
		m.status.State = "idle"
		m.status.Message = "已是最新版本"
	}
	status := m.status
	m.mu.Unlock()
	m.persistCheckedAt()
	return status, nil
}

func updateCheckAttemptMessage(attempt int) string {
	return fmt.Sprintf("正在检查新版本（第 %d/%d 次）", attempt, updateCheckRetryCount+1)
}

func (m *updateManager) setUpdateCheckAttempt(attempt int) {
	m.mu.Lock()
	if m.status.State == "checking" {
		m.status.Message = updateCheckAttemptMessage(attempt)
	}
	m.mu.Unlock()
}

func (m *updateManager) fetchLatestRelease(ctx context.Context) (githubRelease, error) {
	var lastErr error
	for attempt := 1; attempt <= updateCheckRetryCount+1; attempt++ {
		m.setUpdateCheckAttempt(attempt)
		release, err := m.fetchLatestReleaseAttempt(ctx)
		if err == nil {
			return release, nil
		}
		if ctx.Err() != nil || !isRetryableUpdateCheckError(err) {
			return githubRelease{}, err
		}
		lastErr = err
		if attempt == updateCheckRetryCount+1 {
			break
		}
		if err := waitForUpdateCheckRetry(ctx, updateCheckRetryDelay); err != nil {
			return githubRelease{}, err
		}
	}
	return githubRelease{}, &updateCheckRetriesExhaustedError{last: lastErr}
}

func (m *updateManager) fetchLatestReleaseAttempt(ctx context.Context) (githubRelease, error) {
	checkCtx, cancelCheck := context.WithTimeout(ctx, updateCheckTimeout)
	defer cancelCheck()
	request, err := http.NewRequestWithContext(checkCtx, http.MethodGet, m.apiURL, nil)
	if err != nil {
		return githubRelease{}, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "OfferAtlas-Update-Check")
	response, err := m.client.Do(request)
	if err != nil {
		return githubRelease{}, fmt.Errorf("request GitHub release: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return githubRelease{}, &updateCheckHTTPError{status: response.StatusCode}
	}
	var release githubRelease
	if err := json.NewDecoder(io.LimitReader(response.Body, 4*1024*1024)).Decode(&release); err != nil {
		return githubRelease{}, fmt.Errorf("read GitHub release: %w", err)
	}
	return release, nil
}

func isRetryableUpdateCheckError(err error) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var requestError *url.Error
	if errors.As(err, &requestError) {
		return true
	}
	var responseError *updateCheckHTTPError
	if errors.As(err, &responseError) {
		return responseError.status == http.StatusRequestTimeout || responseError.status == http.StatusTooManyRequests || responseError.status >= http.StatusInternalServerError
	}
	return false
}

func waitForUpdateCheckRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *updateManager) finishCheckError(manual bool, reason error) (AppUpdate, error) {
	m.mu.Lock()
	m.status.State = "failed"
	var exhausted *updateCheckRetriesExhaustedError
	if errors.As(reason, &exhausted) {
		m.status.Message = "新版本检测超时，请检查网络后重试"
	} else {
		m.status.Message = "检查更新失败：" + reason.Error()
	}
	status := m.status
	m.mu.Unlock()
	if manual {
		return status, errors.New(status.Message)
	}
	return status, nil
}

func selectUpdateAsset(assets []githubReleaseAsset) (githubReleaseAsset, bool) {
	for _, asset := range assets {
		if strings.EqualFold(asset.Name, "OfferAtlas-windows-amd64.exe") {
			return asset, true
		}
	}
	for _, asset := range assets {
		if strings.EqualFold(asset.Name, "OfferAtlas.exe") {
			return asset, true
		}
	}
	return githubReleaseAsset{}, false
}

// Release asset API requests avoid an unnecessary direct connection to the
// github.com download page and redirect straight to GitHub's file host. Older
// releases or test servers without an asset ID keep using the public URL.
func downloadURLForAsset(asset githubReleaseAsset) string {
	if asset.ID > 0 {
		return defaultUpdateAssetAPIURL + strconv.FormatInt(asset.ID, 10)
	}
	return asset.BrowserDownloadURL
}

func (m *updateManager) download(ctx context.Context) (AppUpdate, error) {
	m.mu.Lock()
	if m.status.State == "downloading" || m.status.State == "installing" {
		status := m.status
		m.mu.Unlock()
		return status, nil
	}
	if !m.status.Available || m.assetURL == "" {
		m.mu.Unlock()
		return m.snapshot(), errors.New("请先检查到可用的新版本")
	}
	assetURL, digest, version := m.assetURL, m.assetDigest, m.status.LatestVersion
	assetName, assetSize := m.status.AssetName, m.status.AssetSize
	m.status.State = "downloading"
	m.status.Message = "正在下载并校验新版本"
	m.status.DownloadedBytes = 0
	m.mu.Unlock()

	expectedHash, err := parseSHA256Digest(digest)
	if err != nil {
		return m.finishDownloadError(err)
	}
	downloadCtx, cancelDownload := context.WithTimeout(ctx, updateDownloadTimeout)
	defer cancelDownload()
	request, err := http.NewRequestWithContext(downloadCtx, http.MethodGet, assetURL, nil)
	if err != nil {
		return m.finishDownloadError(err)
	}
	request.Header.Set("User-Agent", "OfferAtlas-Updater")
	request.Header.Set("Accept", "application/octet-stream")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	response, err := m.client.Do(request)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return m.finishDownloadError(fmt.Errorf("下载超过 %s，请检查网络后重试", updateDownloadTimeout))
		}
		return m.finishDownloadError(fmt.Errorf("download update: %w", err))
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return m.finishDownloadError(fmt.Errorf("下载更新包时 GitHub 返回 HTTP %d", response.StatusCode))
	}
	if err := os.MkdirAll(m.stagingDir, 0o755); err != nil {
		return m.finishDownloadError(err)
	}
	temporary, err := os.CreateTemp(m.stagingDir, "OfferAtlas-update-*.partial")
	if err != nil {
		return m.finishDownloadError(err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	hash := sha256.New()
	buffer := make([]byte, 128*1024)
	var downloaded int64
	lastReported := time.Now()
	for {
		read, readErr := response.Body.Read(buffer)
		if read > 0 {
			if _, err := temporary.Write(buffer[:read]); err != nil {
				_ = temporary.Close()
				return m.finishDownloadError(fmt.Errorf("write update package: %w", err))
			}
			_, _ = hash.Write(buffer[:read])
			downloaded += int64(read)
			if time.Since(lastReported) >= 120*time.Millisecond {
				m.setDownloadProgress(downloaded, assetSize)
				lastReported = time.Now()
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			_ = temporary.Close()
			if errors.Is(readErr, context.DeadlineExceeded) {
				return m.finishDownloadError(fmt.Errorf("下载超过 %s，请检查网络后重试", updateDownloadTimeout))
			}
			return m.finishDownloadError(fmt.Errorf("read update package: %w", readErr))
		}
	}
	if err := temporary.Close(); err != nil {
		return m.finishDownloadError(err)
	}
	if assetSize > 0 && downloaded != assetSize {
		return m.finishDownloadError(fmt.Errorf("更新包大小不匹配：预期 %d 字节，实际 %d 字节", assetSize, downloaded))
	}
	if actual := hash.Sum(nil); !equalBytes(actual, expectedHash) {
		return m.finishDownloadError(errors.New("更新包 SHA-256 校验失败，已丢弃该文件"))
	}
	if err := verifyWindowsExecutable(temporaryPath); err != nil {
		return m.finishDownloadError(err)
	}
	finalPath := filepath.Join(m.stagingDir, "OfferAtlas-"+safeVersionPathPart(version)+".exe")
	_ = os.Remove(finalPath)
	if err := os.Rename(temporaryPath, finalPath); err != nil {
		return m.finishDownloadError(fmt.Errorf("stage verified update: %w", err))
	}
	m.mu.Lock()
	m.downloadPath = finalPath
	m.status.AssetName = assetName
	m.status.AssetSize = downloaded
	m.status.DownloadedBytes = downloaded
	m.status.State = "downloaded"
	m.status.Message = "新版本已下载并通过完整性校验"
	status := m.status
	m.mu.Unlock()
	return status, nil
}

func (m *updateManager) setDownloadProgress(downloaded, size int64) {
	m.mu.Lock()
	m.status.DownloadedBytes = downloaded
	if size > 0 {
		m.status.AssetSize = size
	}
	m.mu.Unlock()
}

func (m *updateManager) finishDownloadError(reason error) (AppUpdate, error) {
	m.mu.Lock()
	m.status.State = "failed"
	m.status.Message = "下载更新失败：" + reason.Error()
	m.status.DownloadedBytes = 0
	status := m.status
	m.mu.Unlock()
	return status, reason
}

func (m *updateManager) prepareInstall() (updateInstallPlan, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.status.State != "downloaded" || m.downloadPath == "" {
		return updateInstallPlan{}, errors.New("请先下载并完成新版本校验")
	}
	if _, err := os.Stat(m.downloadPath); err != nil {
		return updateInstallPlan{}, fmt.Errorf("已下载的更新包不可用，请重新下载：%w", err)
	}
	if runtime.GOOS != "windows" {
		return updateInstallPlan{}, errors.New("自动安装更新目前仅支持 Windows")
	}
	m.status.State = "installing"
	m.status.Message = "新版本已准备完成，正在安全退出"
	return updateInstallPlan{downloadPath: m.downloadPath, targetPath: m.executablePath}, nil
}

func (m *updateManager) cancelInstall(reason error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.status.State == "installing" {
		m.status.State = "downloaded"
		m.status.Message = "更新未安装：" + reason.Error()
	}
}

func (m *updateManager) launchInstaller(plan updateInstallPlan) error {
	if plan.downloadPath == "" || plan.targetPath == "" {
		return errors.New("更新安装计划无效")
	}
	if _, err := os.Stat(plan.downloadPath); err != nil {
		return fmt.Errorf("更新包不可用：%w", err)
	}
	if err := os.MkdirAll(m.stagingDir, 0o755); err != nil {
		return err
	}
	script, err := os.CreateTemp(m.stagingDir, "install-update-*.ps1")
	if err != nil {
		return err
	}
	scriptPath := script.Name()
	contents := updateHelperScript(os.Getpid(), plan.targetPath, plan.downloadPath)
	if _, err := script.WriteString(contents); err != nil {
		_ = script.Close()
		_ = os.Remove(scriptPath)
		return err
	}
	if err := script.Close(); err != nil {
		_ = os.Remove(scriptPath)
		return err
	}
	if err := m.launch("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", scriptPath); err != nil {
		_ = os.Remove(scriptPath)
		return fmt.Errorf("start update helper: %w", err)
	}
	return nil
}

func updateHelperScript(pid int, targetPath, downloadPath string) string {
	targetDirectory := filepath.Dir(targetPath)
	targetName := filepath.Base(targetPath)
	return fmt.Sprintf(`$ErrorActionPreference = 'Stop'
$oldPid = %d
$target = %s
$source = %s
$targetDirectory = %s
$replacement = Join-Path $targetDirectory %s
$previous = Join-Path $targetDirectory %s
while (Get-Process -Id $oldPid -ErrorAction SilentlyContinue) { Start-Sleep -Milliseconds 200 }
try {
  Remove-Item -LiteralPath $replacement -Force -ErrorAction SilentlyContinue
  Copy-Item -LiteralPath $source -Destination $replacement -Force
  if (Test-Path -LiteralPath $target) {
    Remove-Item -LiteralPath $previous -Force -ErrorAction SilentlyContinue
    Move-Item -LiteralPath $target -Destination $previous -Force
  }
  Move-Item -LiteralPath $replacement -Destination $target -Force
  Start-Process -FilePath $target
} catch {
  if (!(Test-Path -LiteralPath $target) -and (Test-Path -LiteralPath $previous)) {
    Move-Item -LiteralPath $previous -Destination $target -Force
  }
  if (Test-Path -LiteralPath $target) { Start-Process -FilePath $target }
  exit 1
}
`, pid, powerShellLiteral(targetPath), powerShellLiteral(downloadPath), powerShellLiteral(targetDirectory), powerShellLiteral(targetName+".updating"), powerShellLiteral(targetName+".previous"))
}

func powerShellLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func parseSHA256Digest(digest string) ([]byte, error) {
	prefix, encoded, found := strings.Cut(strings.TrimSpace(digest), ":")
	if !found || !strings.EqualFold(prefix, "sha256") {
		return nil, errors.New("发布包未提供 SHA-256 完整性校验，无法安全安装")
	}
	decoded, err := hex.DecodeString(encoded)
	if err != nil || len(decoded) != sha256.Size {
		return nil, errors.New("发布包 SHA-256 格式无效")
	}
	return decoded, nil
}

func verifyWindowsExecutable(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	var header [2]byte
	if _, err := io.ReadFull(file, header[:]); err != nil {
		return errors.New("更新包不是有效的 Windows 程序")
	}
	if header != [2]byte{'M', 'Z'} {
		return errors.New("更新包不是有效的 Windows 程序")
	}
	return nil
}

func equalBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var result byte
	for index := range left {
		result |= left[index] ^ right[index]
	}
	return result == 0
}

func normalizeVersion(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "v")
	value = strings.TrimPrefix(value, "V")
	if value == "" {
		return ""
	}
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

func versionGreater(left, right string) bool {
	leftParts, rightParts := strings.Split(normalizeVersion(left), "."), strings.Split(normalizeVersion(right), ".")
	if len(leftParts) != 3 || len(rightParts) != 3 {
		return false
	}
	for index := range leftParts {
		leftNumber, _ := strconv.Atoi(leftParts[index])
		rightNumber, _ := strconv.Atoi(rightParts[index])
		if leftNumber != rightNumber {
			return leftNumber > rightNumber
		}
	}
	return false
}

func safeVersionPathPart(version string) string {
	return strings.ReplaceAll(normalizeVersion(version), ".", "-")
}
