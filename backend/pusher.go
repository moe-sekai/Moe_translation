package backend

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// ============================================================================
// Pusher — pushes translation JSONs to MoeSekai-Hub via GitHub Contents API
//
// Replaces the old approach:
//   github.go (workflow dispatch) → sync-translations-from-deploy.yml (curl loop)
// Now: direct file push, no intermediate GitHub Actions needed.
// ============================================================================

type Pusher struct {
	mu       sync.Mutex
	token    string
	repo     string // "owner/repo" e.g. "moe-sekai/MoeSekai-Hub"
	basePath string // path prefix in repo, e.g. "translation"
	branch   string
	client   *http.Client

	lastPush  time.Time
	lastError string
	pushing   bool
}

type PushStatus struct {
	LastPush  string `json:"lastPush"`
	LastError string `json:"lastError,omitempty"`
	Pushing   bool   `json:"pushing"`
}

func NewPusher(token, repo, basePath, branch string) *Pusher {
	if branch == "" {
		branch = "main"
	}
	return &Pusher{
		token:    token,
		repo:     repo,
		basePath: basePath,
		branch:   branch,
		client:   &http.Client{Timeout: 30 * time.Second},
	}
}

func (p *Pusher) Status() PushStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := PushStatus{Pushing: p.pushing, LastError: p.lastError}
	if !p.lastPush.IsZero() {
		s.LastPush = p.lastPush.Format(time.RFC3339)
	}
	return s
}

// PushAll pushes all translation .json files to the target repo.
// Only pushes the flat format (.json) since that's what the main site consumes.
func (p *Pusher) PushAll(store *Store, username string) error {
	p.mu.Lock()
	if p.pushing {
		p.mu.Unlock()
		return fmt.Errorf("push already in progress")
	}
	p.pushing = true
	p.mu.Unlock()

	defer func() {
		p.mu.Lock()
		p.pushing = false
		p.mu.Unlock()
	}()

	if p.token == "" || p.repo == "" {
		err := fmt.Errorf("GitHub token or repo not configured")
		p.mu.Lock()
		p.lastError = err.Error()
		p.mu.Unlock()
		return err
	}

	var pushErrors []string
	for _, cat := range SupportedCategories {
		flatData, err := store.FlatJSON(cat)
		if err != nil {
			pushErrors = append(pushErrors, fmt.Sprintf("%s: %v", cat, err))
			continue
		}
		remotePath := fmt.Sprintf("%s/%s.json", p.basePath, cat)
		if err := p.pushFile(remotePath, flatData, fmt.Sprintf("update %s.json by %s", cat, username)); err != nil {
			pushErrors = append(pushErrors, fmt.Sprintf("%s.json: %v", cat, err))
		}
	}

	p.mu.Lock()
	p.lastPush = time.Now()
	if len(pushErrors) > 0 {
		p.lastError = fmt.Sprintf("%d errors: %s", len(pushErrors), pushErrors[0])
	} else {
		p.lastError = ""
	}
	p.mu.Unlock()

	if len(pushErrors) > 0 {
		return fmt.Errorf("%d push errors", len(pushErrors))
	}
	fmt.Printf("[push] all %d categories pushed to %s by %s\n", len(SupportedCategories), p.repo, username)
	return nil
}

// pushFile creates or updates a single file in the GitHub repo.
func (p *Pusher) pushFile(path string, content []byte, message string) error {
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/contents/%s", p.repo, path)

	// Get current file info (SHA and content)
	sha, existingContent := p.getFileInfo(apiURL)
	newBase64 := base64.StdEncoding.EncodeToString(content)

	// Check if the content is exactly the same to avoid useless commits
	if sha != "" && existingContent == newBase64 {
		fmt.Printf("[push] Skipping %s (no changes)\n", path)
		return nil
	}

	body := map[string]string{
		"message": message,
		"content": newBase64,
		"branch":  p.branch,
	}
	if sha != "" {
		body["sha"] = sha
	}

	jsonBody, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPut, apiURL, bytes.NewReader(jsonBody))
	req.Header.Set("Authorization", "Bearer "+p.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("GitHub API %d: %s", resp.StatusCode, string(respBody[:min(len(respBody), 200)]))
	}
	return nil
}

// getFileInfo retrieves the SHA and base64 encoded content of a file from GitHub.
func (p *Pusher) getFileInfo(apiURL string) (string, string) {
	req, _ := http.NewRequest(http.MethodGet, apiURL+"?ref="+p.branch, nil)
	req.Header.Set("Authorization", "Bearer "+p.token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := p.client.Do(req)
	if err != nil || resp.StatusCode != 200 {
		return "", ""
	}
	defer resp.Body.Close()

	var result struct {
		SHA     string `json:"sha"`
		Content string `json:"content"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	
	// GitHub API returns Content with newlines (\n), so we remove them for comparison
	cleanContent := ""
	for _, c := range result.Content {
		if c != '\n' && c != '\r' {
			cleanContent += string(c)
		}
	}
	return result.SHA, cleanContent
}
