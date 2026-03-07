package backend

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Pusher struct {
	mu sync.Mutex

	repoURL   string
	branch    string
	workspace string
	dataPath  string

	lastPush  time.Time
	lastError string
	pushing   bool
}

type PushStatus struct {
	LastPush  string `json:"lastPush,omitempty"`
	LastError string `json:"lastError,omitempty"`
	Pushing   bool   `json:"pushing"`
}

func NewPusher(repoURL, branch, workspace, dataPath string) *Pusher {
	if branch == "" {
		branch = "backup-translations"
	}
	if workspace == "" {
		workspace = "/app/git-workspace"
	}
	if dataPath == "" {
		dataPath = "./translations"
	}
	return &Pusher{
		repoURL:   repoURL,
		branch:    branch,
		workspace: workspace,
		dataPath:  dataPath,
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

	if strings.TrimSpace(p.repoURL) == "" {
		err := fmt.Errorf("GIT_PUSH_REPO_URL is not configured")
		p.setError(err)
		return err
	}

	if err := os.MkdirAll(p.workspace, 0o755); err != nil {
		p.setError(err)
		return err
	}

	if store == nil {
		err := fmt.Errorf("store is required")
		p.setError(err)
		return err
	}

	const maxRounds = 3
	for round := 1; round <= maxRounds; round++ {
		startRev := store.CurrentRevision()

		repoDir := filepath.Join(p.workspace, "repo")
		_ = os.RemoveAll(repoDir)

		if err := p.runGit(p.workspace, "clone", "--depth", "1", "--branch", p.branch, p.repoURL, repoDir); err != nil {
			p.setError(err)
			return err
		}

		if err := p.runGit(repoDir, "config", "user.name", "MoeSekai Bot"); err != nil {
			p.setError(err)
			return err
		}
		if err := p.runGit(repoDir, "config", "user.email", "bot@moesekai.com"); err != nil {
			p.setError(err)
			return err
		}

		targetTranslations := filepath.Join(repoDir, "translations")
		store.mu.RLock()
		copyErr := copyDir(p.dataPath, targetTranslations)
		store.mu.RUnlock()
		if copyErr != nil {
			p.setError(copyErr)
			return copyErr
		}

		if err := p.runGit(repoDir, "add", "translations"); err != nil {
			p.setError(err)
			return err
		}

		msg := fmt.Sprintf("chore: backup translations by %s", username)
		if round > 1 {
			msg = fmt.Sprintf("chore: backup translations by %s (catch-up %d)", username, round-1)
		}
		commitErr := p.runGit(repoDir, "commit", "-m", msg)
		if commitErr != nil {
			if !(strings.Contains(commitErr.Error(), "nothing to commit") || strings.Contains(commitErr.Error(), "working tree clean")) {
				p.setError(commitErr)
				return commitErr
			}
		} else {
			if err := p.runGit(repoDir, "push", "origin", p.branch); err != nil {
				p.setError(err)
				return err
			}
		}

		endRev := store.CurrentRevision()
		if endRev == startRev {
			p.mu.Lock()
			p.lastPush = time.Now()
			p.lastError = ""
			p.mu.Unlock()
			return nil
		}
	}

	err := fmt.Errorf("translations changed continuously during backup; retry when edits are stable")
	p.setError(err)
	return err
}

func (p *Pusher) PullLatest(store *Store, username string) error {
	p.mu.Lock()
	if p.pushing {
		p.mu.Unlock()
		return fmt.Errorf("backup sync already in progress")
	}
	p.pushing = true
	p.mu.Unlock()

	defer func() {
		p.mu.Lock()
		p.pushing = false
		p.mu.Unlock()
	}()

	if strings.TrimSpace(p.repoURL) == "" {
		err := fmt.Errorf("GIT_PUSH_REPO_URL is not configured")
		p.setError(err)
		return err
	}
	if store == nil {
		err := fmt.Errorf("store is required")
		p.setError(err)
		return err
	}
	if err := os.MkdirAll(p.workspace, 0o755); err != nil {
		p.setError(err)
		return err
	}

	repoDir := filepath.Join(p.workspace, "repo-pull")
	_ = os.RemoveAll(repoDir)

	if err := p.runGit(p.workspace, "clone", "--depth", "1", "--branch", p.branch, p.repoURL, repoDir); err != nil {
		p.setError(err)
		return err
	}

	remoteTranslations := filepath.Join(repoDir, "translations")
	if info, err := os.Stat(remoteTranslations); err != nil || !info.IsDir() {
		pullErr := fmt.Errorf("remote branch %s has no translations directory", p.branch)
		p.setError(pullErr)
		return pullErr
	}

	stagingPath := filepath.Join(p.workspace, "translations-pull-staging")
	if err := copyDir(remoteTranslations, stagingPath); err != nil {
		p.setError(err)
		return err
	}

	if err := p.replaceDataDir(stagingPath); err != nil {
		p.setError(err)
		return err
	}

	store.ReloadAllFromDisk()

	p.mu.Lock()
	p.lastError = ""
	p.mu.Unlock()

	fmt.Printf("[backup] pulled latest translations from %s by %s\n", p.branch, username)
	return nil
}

func (p *Pusher) setError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastError = err.Error()
}

func (p *Pusher) runGit(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		msg = sanitizeRepoURL(msg)
		return fmt.Errorf("git %s failed: %s", strings.Join(args, " "), msg)
	}
	return nil
}

func sanitizeRepoURL(s string) string {
	if strings.Contains(s, "@github.com") && strings.Contains(s, "https://") {
		start := strings.Index(s, "https://")
		at := strings.Index(s[start:], "@github.com")
		if start >= 0 && at > 0 {
			at = start + at
			prefix := s[:start+8]
			suffix := s[at:]
			return prefix + "***" + suffix
		}
	}
	return s
}

func (p *Pusher) replaceDataDir(src string) error {
	incomingPath := p.dataPath + ".incoming"
	backupPath := p.dataPath + ".backup"

	if err := copyDir(src, incomingPath); err != nil {
		return err
	}
	_ = os.RemoveAll(backupPath)

	if _, err := os.Stat(p.dataPath); err == nil {
		if err := os.Rename(p.dataPath, backupPath); err != nil {
			return err
		}
	}

	if err := os.Rename(incomingPath, p.dataPath); err != nil {
		if _, rollbackErr := os.Stat(backupPath); rollbackErr == nil {
			_ = os.Rename(backupPath, p.dataPath)
		}
		return err
	}

	_ = os.RemoveAll(backupPath)
	return nil
}

func copyDir(src, dst string) error {
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.Create(target)
		if err != nil {
			return err
		}
		defer out.Close()
		if _, err := io.Copy(out, in); err != nil {
			return err
		}
		return out.Chmod(info.Mode())
	})
}
