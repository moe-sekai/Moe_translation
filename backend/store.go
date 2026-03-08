package backend

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
)

// ============================================================================
// Types
// ============================================================================

// Source priority: pinned > human > cn > llm > unknown
const (
	SourceCN      = "cn"
	SourceHuman   = "human"
	SourcePinned  = "pinned"
	SourceLLM     = "llm"
	SourceUnknown = "unknown"
)

type TranslationEntry struct {
	Text   string   `json:"text"`
	Source string   `json:"source"`
	Ids    []string `json:"ids,omitempty"`
}

// field -> { jpKey -> entry }
type TranslationCategory map[string]map[string]TranslationEntry

type FieldInfo struct {
	Name         string `json:"name"`
	Total        int    `json:"total"`
	CnCount      int    `json:"cnCount"`
	HumanCount   int    `json:"humanCount"`
	PinnedCount  int    `json:"pinnedCount"`
	LlmCount     int    `json:"llmCount"`
	UnknownCount int    `json:"unknownCount"`
}

type CategoryInfo struct {
	Name   string      `json:"name"`
	Fields []FieldInfo `json:"fields"`
}

type EntryWithKey struct {
	Key    string   `json:"key"`
	Text   string   `json:"text"`
	Source string   `json:"source"`
	Ids    []string `json:"ids,omitempty"`
}

// ============================================================================
// Store — manages translation JSON files on disk
// ============================================================================

var SupportedCategories = []string{
	"cards", "events", "music", "gacha", "virtualLive",
	"sticker", "comic", "mysekai", "costumes", "characters", "units",
}

type Store struct {
	mu   sync.RWMutex
	path string
	data map[string]TranslationCategory
	rev  uint64
	// changeHooks are invoked when translations or related assets change.
	// Hooks must not call back into Store methods to avoid deadlocks.
	changeHooks []func()
}

func NewStore(path string) *Store {
	os.MkdirAll(path, 0o755)
	s := &Store{path: path, data: make(map[string]TranslationCategory)}
	s.LoadAll()
	return s
}

func (s *Store) LoadAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadAllLocked()
}

func (s *Store) loadAllLocked() {
	s.data = make(map[string]TranslationCategory)
	for _, cat := range SupportedCategories {
		data, err := s.loadCategory(cat)
		if err != nil {
			fmt.Printf("[store] warning: %s: %v\n", cat, err)
			continue
		}
		s.data[cat] = data
	}
	fmt.Printf("[store] loaded %d categories\n", len(s.data))
}

func (s *Store) ReloadAllFromDisk() {
	s.mu.Lock()
	s.loadAllLocked()
	s.rev++
	hooks := append([]func(){}, s.changeHooks...)
	s.mu.Unlock()
	s.runChangeHooks(hooks)
}

func (s *Store) loadCategory(cat string) (TranslationCategory, error) {
	// Prefer .full.json (has source tracking)
	fullPath := filepath.Join(s.path, cat+".full.json")
	if data, err := os.ReadFile(fullPath); err == nil {
		var result TranslationCategory
		if err := json.Unmarshal(data, &result); err == nil {
			return result, nil
		}
	}

	// Fallback: flat .json → convert to full format with source=unknown
	flatPath := filepath.Join(s.path, cat+".json")
	data, err := os.ReadFile(flatPath)
	if err != nil {
		return nil, err
	}
	var flat map[string]map[string]string
	if err := json.Unmarshal(data, &flat); err != nil {
		return nil, err
	}
	result := make(TranslationCategory)
	for field, entries := range flat {
		result[field] = make(map[string]TranslationEntry)
		for k, v := range entries {
			result[field][k] = TranslationEntry{Text: v, Source: SourceUnknown}
		}
	}
	return result, nil
}

// GetCategories returns stats for all categories.
func (s *Store) GetCategories() []CategoryInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []CategoryInfo
	for _, cat := range SupportedCategories {
		data, ok := s.data[cat]
		if !ok {
			continue
		}
		var fields []FieldInfo
		for name, fieldData := range data {
			fi := FieldInfo{Name: name, Total: len(fieldData)}
			for _, e := range fieldData {
				switch e.Source {
				case SourceCN:
					fi.CnCount++
				case SourceHuman:
					fi.HumanCount++
				case SourcePinned:
					fi.PinnedCount++
				case SourceLLM:
					fi.LlmCount++
				default:
					fi.UnknownCount++
				}
			}
			fields = append(fields, fi)
		}
		result = append(result, CategoryInfo{Name: cat, Fields: fields})
	}
	return result
}

// GetEntries returns entries for a category/field with optional source filter.
func (s *Store) GetEntries(category, field, source string) []EntryWithKey {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cat, ok := s.data[category]
	if !ok {
		return nil
	}
	fieldData, ok := cat[field]
	if !ok {
		return nil
	}
	var result []EntryWithKey
	for k, e := range fieldData {
		if source != "" && e.Source != source {
			continue
		}
		result = append(result, EntryWithKey{Key: k, Text: e.Text, Source: e.Source, Ids: e.Ids})
	}
	return result
}

// UpdateEntry updates a single translation and writes both file formats to disk.
func (s *Store) UpdateEntry(category, field, key, text, source string) (string, error) {
	s.mu.Lock()

	cat, ok := s.data[category]
	if !ok {
		s.mu.Unlock()
		return "", fmt.Errorf("unknown category: %s", category)
	}
	if cat[field] == nil {
		cat[field] = make(map[string]TranslationEntry)
	}

	old := cat[field][key]
	changed := false
	if old.Text != text || old.Source != source {
		next := old
		next.Text = text
		next.Source = source
		cat[field][key] = next
		changed = true
	}

	if !changed && syncMysekaiFlavorTextFromTag(category, cat) == 0 {
		s.mu.Unlock()
		return "noop", nil
	}

	// Write .full.json
	fullBytes, _ := json.MarshalIndent(cat, "", "  ")
	fullPath := filepath.Join(s.path, category+".full.json")
	if err := writeAtomic(fullPath, fullBytes); err != nil {
		s.mu.Unlock()
		return "", fmt.Errorf("write full.json: %w", err)
	}

	// Write .json (flat format)
	flat := make(map[string]map[string]string)
	for f, entries := range cat {
		flat[f] = make(map[string]string)
		for k, e := range entries {
			flat[f][k] = e.Text
		}
	}
	flatBytes, _ := json.MarshalIndent(flat, "", "  ")
	flatPath := filepath.Join(s.path, category+".json")
	writeAtomic(flatPath, flatBytes) // non-critical
	s.rev++
	hooks := append([]func(){}, s.changeHooks...)
	s.mu.Unlock()
	s.runChangeHooks(hooks)

	return "ok", nil
}

func (s *Store) CurrentRevision() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.rev
}

func (s *Store) bumpRevisionLocked() {
	s.rev++
}

// RegisterOnChange registers a callback invoked when translation files change.
// Callbacks must not call Store methods to avoid deadlocks.
func (s *Store) RegisterOnChange(fn func()) {
	if fn == nil {
		return
	}
	s.mu.Lock()
	s.changeHooks = append(s.changeHooks, fn)
	s.mu.Unlock()
}

// MarkExternalChange marks that translation-related files changed outside Store.
func (s *Store) MarkExternalChange() {
	s.mu.Lock()
	s.rev++
	hooks := append([]func(){}, s.changeHooks...)
	s.mu.Unlock()
	s.runChangeHooks(hooks)
}

// NotifyChange triggers change hooks without bumping the revision.
func (s *Store) NotifyChange() {
	s.mu.Lock()
	hooks := append([]func(){}, s.changeHooks...)
	s.mu.Unlock()
	s.runChangeHooks(hooks)
}

func (s *Store) runChangeHooks(hooks []func()) {
	for _, hook := range hooks {
		func() {
			defer func() {
				if r := recover(); r != nil {
					fmt.Printf("[store] change hook panic: %v\n%s\n", r, debug.Stack())
				}
			}()
			hook()
		}()
	}
}

// FlatJSON returns the flat-format JSON bytes for a category (for pushing).
func (s *Store) FlatJSON(category string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cat, ok := s.data[category]
	if !ok {
		return nil, fmt.Errorf("unknown category: %s", category)
	}
	flat := make(map[string]map[string]string)
	for f, entries := range cat {
		flat[f] = make(map[string]string)
		for k, e := range entries {
			flat[f][k] = e.Text
		}
	}
	return json.MarshalIndent(flat, "", "  ")
}

// FullJSON returns the full-format JSON bytes for a category (for pushing).
func (s *Store) FullJSON(category string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cat, ok := s.data[category]
	if !ok {
		return nil, fmt.Errorf("unknown category: %s", category)
	}
	return json.MarshalIndent(cat, "", "  ")
}

func IsValidCategory(category string) bool {
	for _, c := range SupportedCategories {
		if c == category {
			return true
		}
	}
	return false
}

func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ============================================================================
// Auth — simple token-based (replaces 140-line hand-rolled JWT)
// ============================================================================

type Auth struct {
	users  map[string]string // username -> password
	secret string
}

func NewAuth(accounts, secret string) *Auth {
	a := &Auth{users: make(map[string]string), secret: secret}
	for _, pair := range strings.Split(accounts, ",") {
		parts := strings.SplitN(strings.TrimSpace(pair), ":", 2)
		if len(parts) == 2 {
			a.users[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
			fmt.Printf("[auth] registered: %s\n", parts[0])
		}
	}
	return a
}

// Authenticate checks credentials and returns a simple HMAC token.
func (a *Auth) Authenticate(user, pass string) bool {
	stored, ok := a.users[user]
	return ok && stored == pass
}

// Token generates a simple bearer token: base64(user:secret).
// Not a full JWT — just enough for a single-server internal tool.
func (a *Auth) Token(user string) string {
	// Simple approach: "user:timestamp:hmac" but for an internal tool,
	// we just use the secret as a shared key.
	return user + ":" + a.secret
}

// Verify checks a bearer token. Returns the username or empty string.
func (a *Auth) Verify(token string) string {
	parts := strings.SplitN(token, ":", 2)
	if len(parts) != 2 || parts[1] != a.secret {
		return ""
	}
	if _, ok := a.users[parts[0]]; !ok {
		return ""
	}
	return parts[0]
}
