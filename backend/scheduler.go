package backend

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ============================================================================
// Scheduler — upstream hash-based trigger
//
// Every 1 h checks the commit hash of the upstream repo branch via GitHub API.
// When the hash changes:
//   - Records the change time
//   - Within the next 3 h, attempts RunOnce every 1 h (max 3 attempts)
//   - After 3 h or 3 successful attempts, waits for the next hash change
// ============================================================================

type SchedulerStatus struct {
	Enabled          bool   `json:"enabled"`
	Running          bool   `json:"running"`
	LastHash         string `json:"lastHash,omitempty"`
	LastCheck        string `json:"lastCheck,omitempty"`
	ChangeDetectedAt string `json:"changeDetectedAt,omitempty"`
	AttemptsUsed     int    `json:"attemptsUsed"`
	LastRun          string `json:"lastRun,omitempty"`
	LastError        string `json:"lastError,omitempty"`
}

type Scheduler struct {
	translator *Translator
	pusher     *Pusher
	store      *Store
	enabled    bool
	httpClient *http.Client

	// upstream repo info
	repoOwner  string // e.g. "Team-Haruki"
	repoName   string // e.g. "haruki-sekai-master"
	repoBranch string // e.g. "main"

	mu     sync.Mutex
	status SchedulerStatus

	// internal tracking
	lastKnownHash    string
	changeDetectedAt time.Time
	attemptsUsed     int
}

func NewScheduler(translator *Translator, pusher *Pusher, store *Store,
	upstreamRepo, upstreamBranch string, enabled bool) *Scheduler {

	// Parse "Team-Haruki/haruki-sekai-master" -> owner + name
	owner, name := "", ""
	parts := strings.SplitN(strings.TrimSpace(upstreamRepo), "/", 2)
	if len(parts) == 2 {
		owner = parts[0]
		name = parts[1]
	}

	s := &Scheduler{
		translator: translator,
		pusher:     pusher,
		store:      store,
		enabled:    enabled,
		httpClient: &http.Client{Timeout: 15 * time.Second},
		repoOwner:  owner,
		repoName:   name,
		repoBranch: upstreamBranch,
	}
	s.status.Enabled = enabled
	return s
}

func (s *Scheduler) Status() SchedulerStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

func (s *Scheduler) Start() {
	if !s.enabled {
		return
	}
	if s.repoOwner == "" || s.repoName == "" {
		fmt.Println("[scheduler] upstream repo not configured, disabled")
		return
	}
	go s.loop()
}

func (s *Scheduler) loop() {
	// Initial check immediately
	s.tick()

	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for range ticker.C {
		s.tick()
	}
}

func (s *Scheduler) tick() {
	hash, err := s.fetchBranchHash()

	s.mu.Lock()
	s.status.LastCheck = time.Now().UTC().Format(time.RFC3339)
	s.mu.Unlock()

	if err != nil {
		fmt.Printf("[scheduler] failed to fetch upstream hash: %v\n", err)
		return
	}

	s.mu.Lock()
	s.status.LastHash = hash

	// First run — just record the hash, don't trigger
	if s.lastKnownHash == "" {
		s.lastKnownHash = hash
		s.mu.Unlock()
		fmt.Printf("[scheduler] initial hash recorded: %s\n", hash[:12])
		return
	}

	// Check if hash changed
	if hash != s.lastKnownHash {
		fmt.Printf("[scheduler] upstream hash changed: %s -> %s\n",
			s.lastKnownHash[:min(12, len(s.lastKnownHash))], hash[:min(12, len(hash))])
		s.lastKnownHash = hash
		s.changeDetectedAt = time.Now()
		s.attemptsUsed = 0
		s.status.ChangeDetectedAt = s.changeDetectedAt.UTC().Format(time.RFC3339)
		s.status.AttemptsUsed = 0
	}
	s.mu.Unlock()

	// If we're within the 3h window and have attempts left, try to run
	s.mu.Lock()
	if s.changeDetectedAt.IsZero() {
		s.mu.Unlock()
		return
	}
	elapsed := time.Since(s.changeDetectedAt)
	attempts := s.attemptsUsed
	s.mu.Unlock()

	if elapsed <= 3*time.Hour && attempts < 3 {
		fmt.Printf("[scheduler] triggering sync (attempt %d/3, %.0f min since change)\n",
			attempts+1, elapsed.Minutes())
		_ = s.RunOnce("hash-change")

		s.mu.Lock()
		s.attemptsUsed++
		s.status.AttemptsUsed = s.attemptsUsed
		s.mu.Unlock()
	}
}

// fetchBranchHash gets the latest commit SHA for the configured branch
// using the GitHub API (no auth needed for public repos).
func (s *Scheduler) fetchBranchHash() (string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/commits/%s",
		s.repoOwner, s.repoName, s.repoBranch)

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github.sha")
	req.Header.Set("User-Agent", "sekai-translate-scheduler")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 400))
		return "", fmt.Errorf("github api %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	// When Accept: application/vnd.github.sha, response is plain text SHA
	contentType := resp.Header.Get("Content-Type")
	if strings.Contains(contentType, "application/json") {
		// Fallback: parse JSON response
		var commit struct {
			SHA string `json:"sha"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&commit); err != nil {
			return "", err
		}
		return commit.SHA, nil
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}

func (s *Scheduler) RunOnce(_ string) error {
	s.mu.Lock()
	if s.status.Running {
		s.mu.Unlock()
		return fmt.Errorf("scheduler job already running")
	}
	s.status.Running = true
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.status.Running = false
		s.status.LastRun = time.Now().UTC().Format(time.RFC3339)
		s.mu.Unlock()
	}()

	_, err := s.translator.SyncCNOnly()
	if err != nil {
		s.mu.Lock()
		s.status.LastError = err.Error()
		s.mu.Unlock()
		return err
	}

	s.mu.Lock()
	s.status.LastError = ""
	s.mu.Unlock()
	return nil
}
