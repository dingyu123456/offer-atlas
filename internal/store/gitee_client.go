package store

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

const defaultGiteeAPIBase = "https://gitee.com/api/v5"

// Gitee permits files up to 50 MiB. Content API reads return Base64 JSON, so
// allow a little over 67 MiB plus response metadata rather than truncating a
// valid large resume or screenshot at the old 12 MiB transport limit.
const maxGiteeResponseBytes = 72 * 1024 * 1024

type giteeClient struct {
	baseURL string
	token   string
	http    *http.Client
}

type giteeUser struct {
	Login string `json:"login"`
	Name  string `json:"name"`
}

type giteeRepo struct {
	Name          string    `json:"name"`
	Path          string    `json:"path"`
	Private       bool      `json:"private"`
	Owner         giteeUser `json:"owner"`
	DefaultBranch string    `json:"default_branch"`
	Size          int64     `json:"size"`
}

type giteeContent struct {
	Type     string `json:"type"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	SHA      string `json:"sha"`
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
	Size     int64  `json:"size"`
}

type giteeError struct {
	Status       int
	Message      string
	TransientWAF bool
}

func (e *giteeError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("Gitee API returned HTTP %d", e.Status)
	}
	return fmt.Sprintf("Gitee API returned HTTP %d: %s", e.Status, e.Message)
}

func (e *giteeError) actionable() bool {
	return e.Status == http.StatusUnauthorized || (e.Status == http.StatusForbidden && !e.TransientWAF) || e.Status == 413 || e.Status == http.StatusInsufficientStorage
}

func newGiteeClient(token string) *giteeClient {
	return &giteeClient{baseURL: defaultGiteeAPIBase, token: strings.TrimSpace(token), http: &http.Client{Timeout: 90 * time.Second}}
}

func (c *giteeClient) withBaseURL(base string) *giteeClient {
	c.baseURL = strings.TrimRight(base, "/")
	return c
}

func (c *giteeClient) request(ctx context.Context, method, endpoint string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.baseURL, "/")+endpoint, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "OfferAtlas/1.0 (+https://gitee.com)")
	// Keep the personal token out of URLs. Besides avoiding accidental exposure
	// in proxies and diagnostics, this is less likely to trigger an edge WAF
	// while a first download performs many file reads.
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	contents, err := io.ReadAll(io.LimitReader(resp.Body, maxGiteeResponseBytes+1))
	if err != nil {
		return err
	}
	if len(contents) > maxGiteeResponseBytes {
		return fmt.Errorf("Gitee 响应超过 %d MiB；请检查单个附件大小", maxGiteeResponseBytes/(1024*1024))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := ""
		var detail struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(contents, &detail) == nil {
			message = detail.Message
		}
		transientWAF := isGiteeWAFResponse(resp.Header.Get("Content-Type"), contents)
		if transientWAF {
			message = "Gitee 暂时拦截了同步请求，将自动重试"
		} else if message == "" {
			message = compactGiteeErrorBody(contents)
		}
		return &giteeError{Status: resp.StatusCode, Message: message, TransientWAF: transientWAF}
	}
	if out == nil || len(contents) == 0 {
		return nil
	}
	return json.Unmarshal(contents, out)
}

func isGiteeWAFResponse(contentType string, contents []byte) bool {
	if !strings.Contains(strings.ToLower(contentType), "text/html") {
		return false
	}
	body := strings.ToLower(string(contents))
	return strings.Contains(body, "baidu_waf_intercept") || strings.Contains(body, "访问存在安全风险")
}

func compactGiteeErrorBody(contents []byte) string {
	message := strings.Join(strings.Fields(string(contents)), " ")
	if message == "" {
		return "请求未返回可用的错误说明"
	}
	if len(message) > 240 {
		return message[:240] + "…"
	}
	return message
}

func (c *giteeClient) currentUser(ctx context.Context) (giteeUser, error) {
	var user giteeUser
	if err := c.request(ctx, http.MethodGet, "/user", nil, &user); err != nil {
		return giteeUser{}, err
	}
	if strings.TrimSpace(user.Login) == "" {
		return giteeUser{}, errors.New("Gitee did not return an account name")
	}
	return user, nil
}

func (c *giteeClient) listRepos(ctx context.Context) ([]giteeRepo, error) {
	items := []giteeRepo{}
	// The official endpoint is paginated. A 100-item page covers normal usage;
	// retain pagination so media repositories keep working for long-running data.
	for page := 1; ; page++ {
		endpoint := fmt.Sprintf("/user/repos?page=%d&per_page=100&sort=full_name&direction=asc", page)
		var batch []giteeRepo
		if err := c.request(ctx, http.MethodGet, endpoint, nil, &batch); err != nil {
			return nil, err
		}
		items = append(items, batch...)
		if len(batch) < 100 {
			return items, nil
		}
	}
}

func (c *giteeClient) getRepo(ctx context.Context, owner, name string) (giteeRepo, error) {
	var repo giteeRepo
	err := c.request(ctx, http.MethodGet, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name), nil, &repo)
	return repo, err
}

func (c *giteeClient) createPrivateRepo(ctx context.Context, name, description string) (giteeRepo, error) {
	var repo giteeRepo
	err := c.request(ctx, http.MethodPost, "/user/repos", map[string]any{
		"name": name, "description": description, "private": true, "auto_init": true,
	}, &repo)
	return repo, err
}

func (c *giteeClient) deleteRepo(ctx context.Context, owner, name string) error {
	return c.request(ctx, http.MethodDelete, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name), nil, nil)
}

func (c *giteeClient) getContent(ctx context.Context, owner, repo, remotePath string) (giteeContent, error) {
	var raw json.RawMessage
	err := c.request(ctx, http.MethodGet, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo)+"/contents/"+escapeRemotePath(remotePath), nil, &raw)
	if err != nil {
		return giteeContent{}, err
	}
	// Gitee normally returns 404 for a missing file. Some repository/content
	// routes instead resolve to the parent directory and return an array. A
	// caller of readFile must never interpret that directory listing as a file;
	// treating it as absent lets optional sync metadata safely fall back.
	if strings.HasPrefix(strings.TrimSpace(string(raw)), "[") {
		return giteeContent{}, &giteeError{Status: http.StatusNotFound, Message: fmt.Sprintf("Gitee returned a directory where a file was expected: %s", remotePath)}
	}
	var content giteeContent
	if err := json.Unmarshal(raw, &content); err != nil {
		return giteeContent{}, fmt.Errorf("decode Gitee file response: %w", err)
	}
	return content, nil
}

func (c *giteeClient) listDirectory(ctx context.Context, owner, repo, remotePath string) ([]giteeContent, error) {
	// Some Gitee deployments paginate repository contents while others ignore
	// page parameters and keep returning the first page. Keep walking genuine
	// pages, but stop as soon as a page contains no unseen path so a long-lived
	// operation directory can never leave the sync worker in a request loop.
	all := make([]giteeContent, 0)
	seen := map[string]bool{}
	base := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/contents/" + escapeRemotePath(remotePath)
	for page := 1; ; page++ {
		var contents []giteeContent
		endpoint := fmt.Sprintf("%s?page=%d&per_page=100", base, page)
		if err := c.request(ctx, http.MethodGet, endpoint, nil, &contents); err != nil {
			return nil, err
		}
		added := 0
		for _, content := range contents {
			key := content.Path
			if key == "" {
				key = content.Type + ":" + content.Name
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			all = append(all, content)
			added++
		}
		if len(contents) < 100 || added == 0 {
			return all, nil
		}
	}
}

func (c *giteeClient) readFile(ctx context.Context, owner, repo, remotePath string) ([]byte, string, error) {
	content, err := c.getContent(ctx, owner, repo, remotePath)
	if err != nil {
		return nil, "", err
	}
	encoded := strings.ReplaceAll(content.Content, "\n", "")
	if content.Encoding != "" && content.Encoding != "base64" {
		return nil, "", fmt.Errorf("unsupported Gitee content encoding %q", content.Encoding)
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, "", fmt.Errorf("decode Gitee file %s: %w", remotePath, err)
	}
	return data, content.SHA, nil
}

func (c *giteeClient) writeFile(ctx context.Context, owner, repo, remotePath, message string, contents []byte, sha string) error {
	body := map[string]any{
		"content": base64.StdEncoding.EncodeToString(contents),
		"message": message,
	}
	method := http.MethodPost
	if sha != "" {
		body["sha"] = sha
		method = http.MethodPut
	}
	// Gitee uses POST to create a new repository file. PUT is exclusively the
	// replace endpoint and rejects an empty SHA, which is why immutable sync
	// operations and first-time repository markers must take this branch.
	return c.request(ctx, method, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo)+"/contents/"+escapeRemotePath(remotePath), body, nil)
}

func escapeRemotePath(remotePath string) string {
	clean := path.Clean(strings.TrimPrefix(remotePath, "/"))
	if clean == "." || strings.HasPrefix(clean, "../") {
		return ""
	}
	parts := strings.Split(clean, "/")
	for index, part := range parts {
		parts[index] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}
