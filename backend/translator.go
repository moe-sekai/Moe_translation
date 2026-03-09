package backend

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	jpMasterdataURL      = "https://sekaimaster.exmeaning.com/master"
	cnMasterdataURL      = "https://sekaimaster-cn.exmeaning.com/master"
	jpAssetsURL          = "https://snowyassets.exmeaning.com/ondemand"
	jpAssetsFallbackURL  = "https://assets.unipjsk.com/ondemand"
	cnAssetsURL          = "https://sekai-assets-bdf29c81.seiunx.net/cn-assets/ondemand"
)

const gameContextPrompt = "你是一个专业的游戏翻译器，专门翻译《世界计划 彩色舞台 feat. 初音未来》(Project SEKAI) 游戏内容。\n请将以下XML格式的日文文本翻译成简体中文。\n请只返回<translations>...</translations>，每条使用 <t id=\"N\">文本</t>。\n"

type TranslatorConfig struct {
	LLMType        string
	GeminiAPIKey   string
	GeminiModel    string
	OpenAIAPIKey   string
	OpenAIBaseURL  string
	OpenAIModel    string
	BatchSize      int
	RateLimitDelay time.Duration
}

type TranslateStatus struct {
	Running   bool   `json:"running"`
	LastRun   string `json:"lastRun,omitempty"`
	LastMode  string `json:"lastMode,omitempty"`
	LastError string `json:"lastError,omitempty"`
	LastNote  string `json:"lastNote,omitempty"`
}

type TranslateResult struct {
	Mode            string   `json:"mode"`
	Categories      int      `json:"categories"`
	UpdatedEntries  int      `json:"updatedEntries"`
	EventStoryFiles int      `json:"eventStoryFiles"`
	Skipped         []string `json:"skipped,omitempty"`
}

type AITranslateRequest struct {
	Category string `json:"category"`
	Field    string `json:"field"`
	Provider string `json:"provider"`
	Limit    int    `json:"limit"`
}

type AITranslateResult struct {
	Category        string `json:"category"`
	Field           string `json:"field"`
	Provider        string `json:"provider"`
	Candidates      int    `json:"candidates"`
	Translated      int    `json:"translated"`
	SkippedExisting int    `json:"skippedExisting"`
}

type AITranslateAllResult struct {
	Provider        string `json:"provider"`
	TotalFields     int    `json:"totalFields"`
	TotalCandidates int    `json:"totalCandidates"`
	TotalTranslated int    `json:"totalTranslated"`
	TotalSkipped    int    `json:"totalSkipped"`
	Errors          int    `json:"errors"`
}

type EventStorySummary struct {
	EventID      int    `json:"eventId"`
	Source       string `json:"source"`
	EpisodeCount int    `json:"episodeCount"`
	LastUpdated  int64  `json:"lastUpdated"`
}

type EventStoryMeta struct {
	Source      string `json:"source"`
	Version     string `json:"version"`
	LastUpdated int64  `json:"last_updated"`
}

type EventStoryEpisode struct {
	ScenarioID  string            `json:"scenarioId"`
	Title       string            `json:"title"`
	TitleSource string            `json:"titleSource,omitempty"`
	TalkData    map[string]string `json:"talkData"`
	TalkSources map[string]string `json:"talkSources,omitempty"`
}

type EventStoryDetail struct {
	Meta     EventStoryMeta               `json:"meta"`
	Episodes map[string]EventStoryEpisode `json:"episodes"`
}

type eventStoryEpisodePayload struct {
	ScenarioID string            `json:"scenarioId"`
	Title      string            `json:"title"`
	TalkData   map[string]string `json:"talkData"`
}

type eventStoryLinePayload struct {
	Text   string `json:"text"`
	Source string `json:"source"`
}

type eventStoryFullEpisodePayload struct {
	ScenarioID string                           `json:"scenarioId"`
	Title      eventStoryLinePayload            `json:"title"`
	TalkData   map[string]eventStoryLinePayload `json:"talkData"`
}

type eventStoryFullDetail struct {
	Meta     EventStoryMeta                          `json:"meta"`
	Episodes map[string]eventStoryFullEpisodePayload `json:"episodes"`
}

type eventStoryTranslateTarget struct {
	EpisodeNo string
	EntryType string
	JP        string
}

type localEventStoryState struct {
	EventID       int
	Source        string
	IsOfficialCN  bool
	IsLLM         bool
	HasCompanion  bool
	PreserveLocal bool
}

type Translator struct {
	store  *Store
	client *http.Client
	cfg    TranslatorConfig

	mu     sync.Mutex
	status TranslateStatus
}

func NewTranslator(store *Store, cfg TranslatorConfig) *Translator {
	if cfg.LLMType == "" {
		cfg.LLMType = "gemini"
	}
	if cfg.GeminiModel == "" {
		cfg.GeminiModel = "gemini-2.0-flash"
	}
	if cfg.OpenAIBaseURL == "" {
		cfg.OpenAIBaseURL = "https://api.openai.com/v1"
	}
	if cfg.OpenAIModel == "" {
		cfg.OpenAIModel = "gpt-4o-mini"
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 20
	}
	if cfg.RateLimitDelay <= 0 {
		cfg.RateLimitDelay = 800 * time.Millisecond
	}
	return &Translator{
		store:  store,
		client: &http.Client{Timeout: 40 * time.Second},
		cfg:    cfg,
	}
}

func (t *Translator) Status() TranslateStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.status
}

func (t *Translator) markStart(mode string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.status.Running {
		return fmt.Errorf("translation task already running")
	}
	t.status.Running = true
	t.status.LastMode = mode
	t.status.LastError = ""
	t.status.LastNote = "running: " + mode
	return nil
}

func (t *Translator) setRunningNote(note string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.status.Running {
		t.status.LastNote = note
	}
}

func (t *Translator) markEnd(note string, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.status.Running = false
	t.status.LastRun = time.Now().UTC().Format(time.RFC3339)
	t.status.LastNote = note
	if err != nil {
		t.status.LastError = err.Error()
	} else {
		t.status.LastError = ""
	}
}

func (t *Translator) SyncCNOnly() (TranslateResult, error) {
	if err := t.markStart("cn-sync"); err != nil {
		return TranslateResult{}, err
	}
	startAt := time.Now()
	fmt.Printf("[translate] cn-sync started\n")
	result := TranslateResult{Mode: "cn-sync"}
	var runErr error
	defer func() {
		note := "cn sync complete"
		if runErr != nil {
			note = "cn sync failed"
			fmt.Printf("[translate] cn-sync failed after %s: %v\n", time.Since(startAt).Round(time.Millisecond), runErr)
		} else {
			fmt.Printf("[translate] cn-sync completed in %s: categories=%d updated=%d eventStories=%d\n",
				time.Since(startAt).Round(time.Millisecond), result.Categories, result.UpdatedEntries, result.EventStoryFiles)
		}
		t.markEnd(note, runErr)
	}()

	all := []struct {
		category string
		fn       func() (map[string]map[string]string, TraceMap, error)
	}{
		{"cards", t.extractCards},
		{"events", t.extractEvents},
		{"gacha", t.extractGacha},
		{"virtualLive", t.extractVirtualLive},
		{"sticker", t.extractStickers},
		{"comic", t.extractComics},
		{"mysekai", t.extractMysekai},
		{"costumes", t.extractCostumes},
		{"characters", t.extractCharacters},
		{"units", t.extractUnits},
		{"music", t.extractMusic},
	}

	for idx, item := range all {
		stepNote := fmt.Sprintf("cn-sync %d/%d: %s", idx+1, len(all), item.category)
		t.setRunningNote(stepNote)
		fmt.Printf("[translate] %s started\n", stepNote)
		fields, trace, err := item.fn()
		if err != nil {
			if isTransientErr(err) {
				fmt.Printf("[translate] %s skipped (transient error: %v)\n", stepNote, err)
				result.Skipped = append(result.Skipped, item.category)
				continue
			}
			runErr = fmt.Errorf("%s: %w", item.category, err)
			return result, runErr
		}
		updated, err := t.applyCategoryCNOnly(item.category, fields, trace)
		if err != nil {
			runErr = fmt.Errorf("apply %s: %w", item.category, err)
			return result, runErr
		}
		result.Categories++
		result.UpdatedEntries += updated
		fmt.Printf("[translate] %s completed, updated=%d\n", stepNote, updated)
	}

	t.setRunningNote("cn-sync event stories")
	fmt.Printf("[translate] cn-sync event stories started\n")
	storyCount, err := t.syncEventStoriesCNOnly()
	if err != nil {
		if isTransientErr(err) {
			fmt.Printf("[translate] cn-sync event stories skipped (transient error: %v)\n", err)
			result.Skipped = append(result.Skipped, "eventStories")
		} else {
			runErr = err
			return result, runErr
		}
	} else {
		result.EventStoryFiles = storyCount
		if storyCount > 0 {
			t.store.MarkExternalChange()
		}
		fmt.Printf("[translate] cn-sync event stories completed, files=%d\n", storyCount)
	}
	return result, nil
}

func (t *Translator) ManualAITranslate(req AITranslateRequest) (AITranslateResult, error) {
	if err := t.markStart("manual-ai"); err != nil {
		return AITranslateResult{}, err
	}
	provider := strings.ToLower(strings.TrimSpace(req.Provider))
	if provider == "" {
		provider = strings.ToLower(strings.TrimSpace(t.cfg.LLMType))
	}
	result := AITranslateResult{Category: req.Category, Field: req.Field, Provider: provider}
	var runErr error
	defer func() { t.markEnd("manual ai complete", runErr) }()

	if req.Category == "" || req.Field == "" {
		runErr = fmt.Errorf("category and field are required")
		return result, runErr
	}
	if !IsValidCategory(req.Category) {
		runErr = fmt.Errorf("unsupported category: %s", req.Category)
		return result, runErr
	}
	if provider != "gemini" && provider != "openai" {
		runErr = fmt.Errorf("unsupported provider: %s", provider)
		return result, runErr
	}

	limit := req.Limit
	if limit <= 0 {
		limit = 200
	}

	t.store.mu.Lock()
	cat, ok := t.store.data[req.Category]
	if !ok {
		t.store.mu.Unlock()
		runErr = fmt.Errorf("category not loaded: %s", req.Category)
		return result, runErr
	}
	fieldMap := cat[req.Field]
	if fieldMap == nil {
		t.store.mu.Unlock()
		runErr = fmt.Errorf("field not found: %s/%s", req.Category, req.Field)
		return result, runErr
	}

	keys := make([]string, 0, len(fieldMap))
	for jp, entry := range fieldMap {
		if entry.Source == SourceHuman || entry.Source == SourcePinned || entry.Source == SourceCN {
			result.SkippedExisting++
			continue
		}
		if strings.TrimSpace(entry.Text) != "" {
			result.SkippedExisting++
			continue
		}
		keys = append(keys, jp)
	}
	t.store.mu.Unlock()
	sort.Strings(keys)
	if len(keys) > limit {
		keys = keys[:limit]
	}
	result.Candidates = len(keys)

	if len(keys) == 0 {
		return result, nil
	}

	updates := make(map[string]string, len(keys))
	for i := 0; i < len(keys); i += t.cfg.BatchSize {
		end := i + t.cfg.BatchSize
		if end > len(keys) {
			end = len(keys)
		}
		batch := keys[i:end]
		translated, err := t.callLLM(provider, batch)
		if err != nil {
			runErr = err
			return result, runErr
		}
		for idx, jp := range batch {
			if idx < len(translated) {
				cn := strings.TrimSpace(translated[idx])
				if cn != "" {
					updates[jp] = cn
				}
			}
		}
		if end < len(keys) {
			time.Sleep(t.cfg.RateLimitDelay)
		}
	}

	t.store.mu.Lock()
	cat = t.store.data[req.Category]
	if cat == nil {
		cat = make(TranslationCategory)
		t.store.data[req.Category] = cat
	}
	if cat[req.Field] == nil {
		cat[req.Field] = make(map[string]TranslationEntry)
	}
	for jp, cn := range updates {
		current := cat[req.Field][jp]
		if current.Source == SourcePinned || current.Source == SourceHuman || current.Source == SourceCN {
			result.SkippedExisting++
			continue
		}
		if strings.TrimSpace(current.Text) != "" && current.Source != SourceUnknown && current.Source != SourceLLM {
			result.SkippedExisting++
			continue
		}
		next := current
		next.Text = cn
		next.Source = SourceLLM
		cat[req.Field][jp] = next
		result.Translated++
	}
	err := t.saveCategoryLocked(req.Category, cat)
	t.store.mu.Unlock()
	if err != nil {
		runErr = err
		return result, runErr
	}
	if result.Translated > 0 {
		t.store.NotifyChange()
	}

	return result, nil
}

func (t *Translator) ListEventStories() ([]EventStorySummary, error) {
	dir := filepath.Join(t.store.path, "eventStory")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []EventStorySummary{}, nil
		}
		return nil, err
	}
	result := make([]EventStorySummary, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if !strings.HasPrefix(e.Name(), "event_") {
			continue
		}
		idText := strings.TrimSuffix(strings.TrimPrefix(e.Name(), "event_"), ".json")
		eventID, err := strconv.Atoi(idText)
		if err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		detail, err := parseEventStoryDetail(data, info.ModTime().Unix())
		if err != nil {
			continue
		}
		s := EventStorySummary{
			EventID:      eventID,
			Source:       detail.Meta.Source,
			EpisodeCount: len(detail.Episodes),
			LastUpdated:  detail.Meta.LastUpdated,
		}
		result = append(result, s)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].EventID > result[j].EventID })
	return result, nil
}

func (t *Translator) GetEventStory(eventID int) (EventStoryDetail, error) {
	var detail EventStoryDetail
	dir := filepath.Join(t.store.path, "eventStory")
	path := eventStoryJSONPath(dir, eventID)
	data, err := os.ReadFile(path)
	if err != nil {
		return detail, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return detail, err
	}
	detail, err = parseEventStoryDetail(data, info.ModTime().Unix())
	if err != nil {
		return detail, err
	}
	fullPath := eventStoryFullJSONPath(dir, eventID)
	fullData, err := os.ReadFile(fullPath)
	if err == nil {
		fullInfo, statErr := os.Stat(fullPath)
		if statErr != nil {
			fmt.Printf("[eventStory] warning: stat sidecar for event %d failed: %v\n", eventID, statErr)
			return detail, nil
		}
		fullDetail, parseErr := parseEventStoryFullDetail(fullData, fullInfo.ModTime().Unix(), detail)
		if parseErr != nil {
			fmt.Printf("[eventStory] warning: parse sidecar for event %d failed: %v\n", eventID, parseErr)
			return detail, nil
		}
		applyEventStoryLineSources(&detail, fullDetail)
	} else if !os.IsNotExist(err) {
		fmt.Printf("[eventStory] warning: read sidecar for event %d failed: %v\n", eventID, err)
		return detail, nil
	}
	return detail, nil
}

// AITranslateAll iterates all loaded categories and fields,
// translating entries that have no translation (empty text, source unknown/llm).
func (t *Translator) AITranslateAll(provider string) (AITranslateAllResult, error) {
	if err := t.markStart("ai-translate-all"); err != nil {
		return AITranslateAllResult{}, err
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		provider = strings.ToLower(strings.TrimSpace(t.cfg.LLMType))
	}
	result := AITranslateAllResult{Provider: provider}
	var runErr error
	defer func() { t.markEnd("ai translate all complete", runErr) }()

	if provider != "gemini" && provider != "openai" {
		runErr = fmt.Errorf("unsupported provider: %s", provider)
		return result, runErr
	}

	// Collect all category/field pairs
	t.store.mu.RLock()
	type catField struct {
		category string
		field    string
	}
	var pairs []catField
	for catName, cat := range t.store.data {
		for fieldName := range cat {
			pairs = append(pairs, catField{catName, fieldName})
		}
	}
	t.store.mu.RUnlock()

	for _, cf := range pairs {
		req := AITranslateRequest{
			Category: cf.category,
			Field:    cf.field,
			Provider: provider,
			Limit:    200,
		}
		// Call ManualAITranslate but reset running state first
		// We do inline logic instead to avoid markStart conflict
		sub, err := t.aiTranslateField(req)
		result.TotalFields++
		result.TotalCandidates += sub.Candidates
		result.TotalTranslated += sub.Translated
		result.TotalSkipped += sub.SkippedExisting
		if err != nil {
			result.Errors++
			fmt.Printf("[ai-all] error on %s/%s: %v\n", cf.category, cf.field, err)
		}
	}

	return result, nil
}

// aiTranslateField is the inner logic of ManualAITranslate without markStart/markEnd.
func (t *Translator) aiTranslateField(req AITranslateRequest) (AITranslateResult, error) {
	provider := strings.ToLower(strings.TrimSpace(req.Provider))
	result := AITranslateResult{Category: req.Category, Field: req.Field, Provider: provider}

	if !IsValidCategory(req.Category) {
		return result, fmt.Errorf("unsupported category: %s", req.Category)
	}

	limit := req.Limit
	if limit <= 0 {
		limit = 200
	}

	t.store.mu.Lock()
	cat, ok := t.store.data[req.Category]
	if !ok {
		t.store.mu.Unlock()
		return result, fmt.Errorf("category not loaded: %s", req.Category)
	}
	fieldMap := cat[req.Field]
	if fieldMap == nil {
		t.store.mu.Unlock()
		return result, nil // no entries
	}

	keys := make([]string, 0, len(fieldMap))
	for jp, entry := range fieldMap {
		if entry.Source == SourceHuman || entry.Source == SourcePinned || entry.Source == SourceCN {
			result.SkippedExisting++
			continue
		}
		if strings.TrimSpace(entry.Text) != "" {
			result.SkippedExisting++
			continue
		}
		keys = append(keys, jp)
	}
	t.store.mu.Unlock()
	sort.Strings(keys)
	if len(keys) > limit {
		keys = keys[:limit]
	}
	result.Candidates = len(keys)

	if len(keys) == 0 {
		return result, nil
	}

	updates := make(map[string]string, len(keys))
	for i := 0; i < len(keys); i += t.cfg.BatchSize {
		end := i + t.cfg.BatchSize
		if end > len(keys) {
			end = len(keys)
		}
		batch := keys[i:end]
		translated, err := t.callLLM(provider, batch)
		if err != nil {
			return result, err
		}
		for idx, jp := range batch {
			if idx < len(translated) {
				cn := strings.TrimSpace(translated[idx])
				if cn != "" {
					updates[jp] = cn
				}
			}
		}
		if end < len(keys) {
			time.Sleep(t.cfg.RateLimitDelay)
		}
	}

	t.store.mu.Lock()
	cat = t.store.data[req.Category]
	if cat == nil {
		cat = make(TranslationCategory)
		t.store.data[req.Category] = cat
	}
	if cat[req.Field] == nil {
		cat[req.Field] = make(map[string]TranslationEntry)
	}
	for jp, cn := range updates {
		current := cat[req.Field][jp]
		if current.Source == SourcePinned || current.Source == SourceHuman || current.Source == SourceCN {
			result.SkippedExisting++
			continue
		}
		if strings.TrimSpace(current.Text) != "" && current.Source != SourceUnknown && current.Source != SourceLLM {
			result.SkippedExisting++
			continue
		}
		next := current
		next.Text = cn
		next.Source = SourceLLM
		cat[req.Field][jp] = next
		result.Translated++
	}
	err := t.saveCategoryLocked(req.Category, cat)
	t.store.mu.Unlock()
	if err != nil {
		return result, err
	}
	if result.Translated > 0 {
		t.store.NotifyChange()
	}

	return result, nil
}

// UpdateEventStoryLine updates a single title or line in an event story file.
func (t *Translator) UpdateEventStoryLine(eventID int, episodeNo, jpKey, cnText, source, entryType string) error {
	dir := filepath.Join(t.store.path, "eventStory")
	path := eventStoryJSONPath(dir, eventID)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	detail, err := parseEventStoryDetail(data, info.ModTime().Unix())
	if err != nil {
		return err
	}
	originalMainData := append([]byte(nil), data...)
	originalFullPath := eventStoryFullJSONPath(dir, eventID)
	originalFullData, originalFullErr := os.ReadFile(originalFullPath)
	fullDetail, err := t.loadOrBuildEventStoryFullDetail(dir, eventID, detail)
	if err != nil {
		return err
	}

	ep, ok := detail.Episodes[episodeNo]
	if !ok {
		return fmt.Errorf("episode %s not found in event %d", episodeNo, eventID)
	}
	fullEpisode, ok := fullDetail.Episodes[episodeNo]
	if !ok {
		return fmt.Errorf("episode %s not found in event %d full state", episodeNo, eventID)
	}
	lineSource := normalizeEventStoryLineSource(source)
	if lineSource == SourceUnknown {
		lineSource = SourceHuman
	}
	normalizedEntryType := strings.TrimSpace(strings.ToLower(entryType))
	lineEntry, exists := fullEpisode.TalkData[jpKey]
	switch normalizedEntryType {
	case "", "talk":
		if _, exists := ep.TalkData[jpKey]; !exists {
			return fmt.Errorf("key not found in episode %s", episodeNo)
		}
		if !exists {
			return fmt.Errorf("key not found in episode %s full state", episodeNo)
		}
		ep.TalkData[jpKey] = cnText
		lineEntry.Text = cnText
		lineEntry.Source = lineSource
		fullEpisode.TalkData[jpKey] = lineEntry
	case "title":
		ep.Title = cnText
		fullEpisode.Title.Text = cnText
		fullEpisode.Title.Source = lineSource
	default:
		return fmt.Errorf("unsupported event story entry type: %s", entryType)
	}
	detail.Episodes[episodeNo] = ep
	fullDetail.Episodes[episodeNo] = fullEpisode
	now := time.Now().Unix()
	detail.Meta.LastUpdated = now
	fullDetail.Meta.LastUpdated = now
	if allEventStoryContentHuman(fullDetail) {
		promoteEventStoryFullDetailToHuman(&fullDetail)
		detail.Meta.Source = SourceHuman
	} else {
		fullDetail.Meta.Source = normalizeEventStoryStorySource(detail.Meta.Source)
	}
	detail.Meta.Version = "1.0"
	fullDetail.Meta.Version = "1.0"

	out, err := json.MarshalIndent(detail, "", "  ")
	if err != nil {
		return err
	}
	if err := writeAtomic(path, out); err != nil {
		return err
	}
	fullOut, err := json.MarshalIndent(fullDetail, "", "  ")
	if err != nil {
		return err
	}
	if err := writeAtomic(originalFullPath, fullOut); err != nil {
		rollbackErr := writeAtomic(path, originalMainData)
		if rollbackErr != nil {
			return fmt.Errorf("write event story full sidecar: %w (rollback failed: %v)", err, rollbackErr)
		}
		if originalFullErr == nil {
			_ = writeAtomic(originalFullPath, originalFullData)
		}
		return err
	}
	t.store.MarkExternalChange()
	return nil
}

func (t *Translator) PromoteEventStoryHuman(eventID int) error {
	dir := filepath.Join(t.store.path, "eventStory")
	path := eventStoryJSONPath(dir, eventID)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	detail, err := parseEventStoryDetail(data, info.ModTime().Unix())
	if err != nil {
		return err
	}
	originalMainData := append([]byte(nil), data...)
	originalFullPath := eventStoryFullJSONPath(dir, eventID)
	originalFullData, originalFullErr := os.ReadFile(originalFullPath)
	fullDetail, err := t.loadOrBuildEventStoryFullDetail(dir, eventID, detail)
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	promoteEventStoryFullDetailToHuman(&fullDetail)
	fullDetail.Meta.LastUpdated = now
	fullDetail.Meta.Version = "1.0"
	detail.Meta.Source = SourceHuman
	detail.Meta.LastUpdated = now
	detail.Meta.Version = "1.0"

	out, err := json.MarshalIndent(detail, "", "  ")
	if err != nil {
		return err
	}
	if err := writeAtomic(path, out); err != nil {
		return err
	}
	fullOut, err := json.MarshalIndent(fullDetail, "", "  ")
	if err != nil {
		return err
	}
	if err := writeAtomic(originalFullPath, fullOut); err != nil {
		rollbackErr := writeAtomic(path, originalMainData)
		if rollbackErr != nil {
			return fmt.Errorf("write event story full sidecar: %w (rollback failed: %v)", err, rollbackErr)
		}
		if originalFullErr == nil {
			_ = writeAtomic(originalFullPath, originalFullData)
		}
		return err
	}
	t.store.MarkExternalChange()
	return nil
}

func parseEventStoryDetail(raw []byte, fallbackLastUpdated int64) (EventStoryDetail, error) {
	var detail EventStoryDetail
	if err := json.Unmarshal(raw, &detail); err == nil && detail.Episodes != nil {
		normalizeEventStoryMeta(&detail.Meta, fallbackLastUpdated)
		return detail, nil
	}

	var legacy map[string]eventStoryEpisodePayload
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return detail, err
	}

	episodes := make(map[string]EventStoryEpisode)
	for episodeNo, episode := range legacy {
		if _, err := strconv.Atoi(episodeNo); err != nil {
			continue
		}
		if episode.ScenarioID == "" && strings.TrimSpace(episode.Title) == "" && len(episode.TalkData) == 0 {
			continue
		}
		episodes[episodeNo] = EventStoryEpisode{
			ScenarioID: episode.ScenarioID,
			Title:      episode.Title,
			TalkData:   episode.TalkData,
		}
	}
	if len(episodes) == 0 {
		return detail, fmt.Errorf("unsupported event story format")
	}

	detail.Meta.Source = "official_cn"
	detail.Meta.Version = "legacy"
	detail.Meta.LastUpdated = fallbackLastUpdated
	detail.Episodes = episodes
	return detail, nil
}

func parseEventStoryFullDetail(raw []byte, fallbackLastUpdated int64, base EventStoryDetail) (eventStoryFullDetail, error) {
	var detail eventStoryFullDetail
	if err := json.Unmarshal(raw, &detail); err != nil {
		return detail, err
	}
	if detail.Episodes == nil {
		return detail, fmt.Errorf("unsupported event story full format")
	}
	normalizeEventStoryMeta(&detail.Meta, fallbackLastUpdated)
	baseSource := normalizeEventStoryLineSource(base.Meta.Source)
	for episodeNo, episode := range base.Episodes {
		fullEpisode, ok := detail.Episodes[episodeNo]
		if !ok {
			fullEpisode = eventStoryFullEpisodePayload{
				ScenarioID: episode.ScenarioID,
				Title: eventStoryLinePayload{
					Text:   episode.Title,
					Source: baseSource,
				},
				TalkData: map[string]eventStoryLinePayload{},
			}
		}
		if fullEpisode.TalkData == nil {
			fullEpisode.TalkData = map[string]eventStoryLinePayload{}
		}
		if strings.TrimSpace(fullEpisode.ScenarioID) == "" {
			fullEpisode.ScenarioID = episode.ScenarioID
		}
		if fullEpisode.Title.Text == "" {
			fullEpisode.Title.Text = episode.Title
		}
		fullEpisode.Title.Source = normalizeEventStoryLineSource(fullEpisode.Title.Source)
		for jp, cn := range episode.TalkData {
			line, ok := fullEpisode.TalkData[jp]
			if !ok {
				line = eventStoryLinePayload{Text: cn, Source: baseSource}
			} else {
				if line.Text == "" && cn == "" {
					line.Text = ""
				} else if line.Text == "" {
					line.Text = cn
				}
				line.Source = normalizeEventStoryLineSource(line.Source)
			}
			fullEpisode.TalkData[jp] = line
		}
		detail.Episodes[episodeNo] = fullEpisode
	}
	return detail, nil
}

func normalizeEventStoryMeta(meta *EventStoryMeta, fallbackLastUpdated int64) {
	meta.Source = normalizeEventStoryStorySource(meta.Source)
	if meta.Version == "" {
		meta.Version = "1.0"
	}
	if meta.LastUpdated == 0 {
		meta.LastUpdated = fallbackLastUpdated
	}
}

func normalizeEventStoryStorySource(source string) string {
	switch strings.TrimSpace(strings.ToLower(source)) {
	case "official_cn", "llm", "jp_pending", SourceHuman, SourcePinned, SourceUnknown:
		return strings.TrimSpace(strings.ToLower(source))
	case "official_cn_legacy", SourceCN:
		return "official_cn"
	case "":
		return SourceUnknown
	default:
		return strings.TrimSpace(strings.ToLower(source))
	}
}

func normalizeEventStoryLineSource(source string) string {
	switch strings.TrimSpace(strings.ToLower(source)) {
	case SourceHuman, SourcePinned, SourceLLM, SourceUnknown, SourceCN:
		return strings.TrimSpace(strings.ToLower(source))
	case "official_cn", "official_cn_legacy":
		return SourceCN
	case "jp_pending", "":
		return SourceUnknown
	default:
		return SourceUnknown
	}
}

func applyEventStoryLineSources(detail *EventStoryDetail, full eventStoryFullDetail) {
	baseSource := normalizeEventStoryLineSource(detail.Meta.Source)
	for episodeNo, episode := range detail.Episodes {
		fullEpisode, ok := full.Episodes[episodeNo]
		talkSources := make(map[string]string, len(episode.TalkData))
		titleSource := baseSource
		if ok {
			if normalized := normalizeEventStoryLineSource(fullEpisode.Title.Source); normalized != SourceUnknown || baseSource == SourceUnknown {
				titleSource = normalized
			}
		}
		for jp := range episode.TalkData {
			source := baseSource
			if ok {
				if line, hasLine := fullEpisode.TalkData[jp]; hasLine {
					source = normalizeEventStoryLineSource(line.Source)
				}
			}
			talkSources[jp] = source
		}
		episode.TitleSource = titleSource
		episode.TalkSources = talkSources
		detail.Episodes[episodeNo] = episode
	}
}

func buildEventStoryFullDetail(detail EventStoryDetail) eventStoryFullDetail {
	baseSource := normalizeEventStoryLineSource(detail.Meta.Source)
	full := eventStoryFullDetail{
		Meta: EventStoryMeta{
			Source:      normalizeEventStoryStorySource(detail.Meta.Source),
			Version:     "1.0",
			LastUpdated: detail.Meta.LastUpdated,
		},
		Episodes: make(map[string]eventStoryFullEpisodePayload, len(detail.Episodes)),
	}
	for episodeNo, episode := range detail.Episodes {
		fullEpisode := eventStoryFullEpisodePayload{
			ScenarioID: episode.ScenarioID,
			Title: eventStoryLinePayload{
				Text:   episode.Title,
				Source: baseSource,
			},
			TalkData: make(map[string]eventStoryLinePayload, len(episode.TalkData)),
		}
		for jp, cn := range episode.TalkData {
			fullEpisode.TalkData[jp] = eventStoryLinePayload{Text: cn, Source: baseSource}
		}
		full.Episodes[episodeNo] = fullEpisode
	}
	return full
}

func (t *Translator) loadOrBuildEventStoryFullDetail(dir string, eventID int, base EventStoryDetail) (eventStoryFullDetail, error) {
	path := eventStoryFullJSONPath(dir, eventID)
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return buildEventStoryFullDetail(base), nil
		}
		return eventStoryFullDetail{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return eventStoryFullDetail{}, err
	}
	return parseEventStoryFullDetail(raw, info.ModTime().Unix(), base)
}

func allEventStoryContentHuman(detail eventStoryFullDetail) bool {
	total := 0
	for _, episode := range detail.Episodes {
		if strings.TrimSpace(episode.Title.Text) != "" {
			total++
			if normalizeEventStoryLineSource(episode.Title.Source) != SourceHuman {
				return false
			}
		}
		for _, line := range episode.TalkData {
			total++
			if normalizeEventStoryLineSource(line.Source) != SourceHuman {
				return false
			}
		}
	}
	return total > 0
}

func promoteEventStoryFullDetailToHuman(detail *eventStoryFullDetail) {
	detail.Meta.Source = SourceHuman
	for episodeNo, episode := range detail.Episodes {
		episode.Title.Source = SourceHuman
		for jp, line := range episode.TalkData {
			line.Source = SourceHuman
			episode.TalkData[jp] = line
		}
		detail.Episodes[episodeNo] = episode
	}
}

func eventStoryFullHasEditableSources(detail eventStoryFullDetail) bool {
	for _, episode := range detail.Episodes {
		if normalizeEventStoryLineSource(episode.Title.Source) == SourceHuman || normalizeEventStoryLineSource(episode.Title.Source) == SourcePinned {
			return true
		}
		for _, line := range episode.TalkData {
			source := normalizeEventStoryLineSource(line.Source)
			if source == SourceHuman || source == SourcePinned {
				return true
			}
		}
	}
	return false
}

func eventStoryJSONPath(dir string, eventID int) string {
	return filepath.Join(dir, fmt.Sprintf("event_%d.json", eventID))
}

func eventStoryFullJSONPath(dir string, eventID int) string {
	return filepath.Join(dir, fmt.Sprintf("event_%d.full.json", eventID))
}

func (t *Translator) applyCategoryCNOnly(category string, fields map[string]map[string]string, trace TraceMap) (int, error) {
	t.store.mu.Lock()

	if t.store.data[category] == nil {
		t.store.data[category] = make(TranslationCategory)
	}
	cat := t.store.data[category]
	updated := 0

	for field, mapping := range fields {
		if cat[field] == nil {
			cat[field] = make(map[string]TranslationEntry)
		}
		for jp, cn := range mapping {
			old, has := cat[field][jp]
			next := old
			if cn != "" {
				if has && old.Source == SourcePinned {
					// Still merge trace IDs for pinned entries
					if traceField := trace[field]; traceField != nil {
						if ids := traceField[jp]; len(ids) > 0 {
							mergeTraceIDs(&next, ids)
							if !idsEqual(old.Ids, next.Ids) {
								cat[field][jp] = next
								updated++
							}
						}
					}
					continue
				}
				next.Text = cn
				next.Source = SourceCN
			} else {
				if has && old.Text != "" {
					// Still merge trace IDs for existing entries
					if traceField := trace[field]; traceField != nil {
						if ids := traceField[jp]; len(ids) > 0 {
							mergeTraceIDs(&next, ids)
							if !idsEqual(old.Ids, next.Ids) {
								cat[field][jp] = next
								updated++
							}
						}
					}
					continue
				}
				next.Text = ""
				next.Source = SourceUnknown
			}
			// Merge trace IDs for new/updated entries
			if traceField := trace[field]; traceField != nil {
				if ids := traceField[jp]; len(ids) > 0 {
					mergeTraceIDs(&next, ids)
				}
			}
			if !has || old.Text != next.Text || old.Source != next.Source || !idsEqual(old.Ids, next.Ids) {
				cat[field][jp] = next
				updated++
			}
		}
	}

	// Merge trace IDs for pre-existing entries not covered by fields mapping
	for field, traceField := range trace {
		if cat[field] == nil {
			continue
		}
		for jp, ids := range traceField {
			if len(ids) == 0 {
				continue
			}
			entry, ok := cat[field][jp]
			if !ok {
				continue
			}
			oldIDs := entry.Ids
			mergeTraceIDs(&entry, ids)
			if !idsEqual(oldIDs, entry.Ids) {
				cat[field][jp] = entry
				updated++
			}
		}
	}

	updated += syncMysekaiFlavorTextFromTag(category, cat)

	if err := t.saveCategoryLocked(category, cat); err != nil {
		t.store.mu.Unlock()
		return updated, err
	}
	t.store.mu.Unlock()
	if updated > 0 {
		t.store.NotifyChange()
	}
	return updated, nil
}

func idsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func syncMysekaiFlavorTextFromTag(category string, cat TranslationCategory) int {
	if category != "mysekai" || cat == nil {
		return 0
	}
	flavorEntries := cat["flavorText"]
	tagEntries := cat["tag"]
	if flavorEntries == nil || tagEntries == nil {
		return 0
	}
	updated := 0
	for jp, flavorEntry := range flavorEntries {
		tagEntry, ok := tagEntries[jp]
		if !ok {
			continue
		}
		if strings.TrimSpace(tagEntry.Text) == "" && tagEntry.Source == SourceUnknown {
			continue
		}
		next := flavorEntry
		next.Text = tagEntry.Text
		next.Source = tagEntry.Source
		if flavorEntry.Text == next.Text && flavorEntry.Source == next.Source {
			continue
		}
		flavorEntries[jp] = next
		updated++
	}
	return updated
}

func (t *Translator) saveCategoryLocked(category string, cat TranslationCategory) error {
	fullBytes, err := json.MarshalIndent(cat, "", "  ")
	if err != nil {
		return err
	}
	fullPath := filepath.Join(t.store.path, category+".full.json")
	if err := writeAtomic(fullPath, fullBytes); err != nil {
		return fmt.Errorf("write %s.full.json: %w", category, err)
	}

	flat := make(map[string]map[string]string, len(cat))
	for field, entries := range cat {
		flat[field] = make(map[string]string, len(entries))
		for jp, entry := range entries {
			flat[field][jp] = entry.Text
		}
	}
	flatBytes, err := json.MarshalIndent(flat, "", "  ")
	if err != nil {
		return err
	}
	flatPath := filepath.Join(t.store.path, category+".json")
	if err := writeAtomic(flatPath, flatBytes); err != nil {
		return fmt.Errorf("write %s.json: %w", category, err)
	}
	t.store.bumpRevisionLocked()
	return nil
}

func (t *Translator) fetchMasterdata(filename, server string) ([]map[string]any, error) {
	base := jpMasterdataURL
	if server == "cn" {
		base = cnMasterdataURL
	}
	url := fmt.Sprintf("%s/%s", base, filename)
	data, err := t.fetchJSONURL(url)
	if err != nil {
		return nil, err
	}
	arr, ok := data.([]any)
	if !ok {
		return nil, fmt.Errorf("unexpected json type for %s", filename)
	}
	out := make([]map[string]any, 0, len(arr))
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if ok {
			out = append(out, m)
		}
	}
	return out, nil
}

// isTransientErr returns true for network errors and 502/503/504 status codes
// that are likely temporary and worth retrying.
func isTransientErr(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	// HTTP status codes that indicate upstream/temporary failures
	for _, code := range []string{"http 502:", "http 503:", "http 504:"} {
		if strings.Contains(s, code) {
			return true
		}
	}
	// net.Error (timeout, connection reset, etc.)
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	return false
}

func (t *Translator) fetchJSONURL(url string) (any, error) {
	const maxRetries = 2
	var lastErr error
	for attempt := range maxRetries + 1 {
		result, err := t.fetchJSONURLOnce(url)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if !isTransientErr(err) {
			return nil, err
		}
		if attempt < maxRetries {
			delay := time.Duration(attempt+1) * 2 * time.Second
			fmt.Printf("[translate] transient error (attempt %d/%d), retrying in %v: %v\n",
				attempt+1, maxRetries+1, delay, err)
			time.Sleep(delay)
		}
	}
	return nil, lastErr
}

// fetchJPAssetJSON fetches a JP asset JSON by path (e.g. "event_story/.../scenario/xxx").
// It tries the primary jpAssetsURL first; on failure it falls back to jpAssetsFallbackURL.
func (t *Translator) fetchJPAssetJSON(assetPath string) (any, error) {
	primaryURL := fmt.Sprintf("%s/%s.json", jpAssetsURL, assetPath)
	result, err := t.fetchJSONURL(primaryURL)
	if err == nil {
		return result, nil
	}
	fallbackURL := fmt.Sprintf("%s/%s.json", jpAssetsFallbackURL, assetPath)
	fmt.Printf("[translate] JP asset primary failed (%v), trying fallback: %s\n", err, fallbackURL)
	result, fallbackErr := t.fetchJSONURL(fallbackURL)
	if fallbackErr != nil {
		return nil, fmt.Errorf("primary: %w; fallback: %v", err, fallbackErr)
	}
	return result, nil
}

// fetchJPScenarioJSON fetches a JP scenario asset and validates that TalkData is present.
// If the primary source returns data with empty/missing TalkData, it falls back to the
// backup source before giving up — this handles cases where the primary CDN has incomplete data.
func (t *Translator) fetchJPScenarioJSON(assetPath string) (any, error) {
	primaryURL := fmt.Sprintf("%s/%s.json", jpAssetsURL, assetPath)
	result, err := t.fetchJSONURL(primaryURL)
	if err == nil {
		if scenarioHasTalkData(result) {
			return result, nil
		}
		// Primary returned OK but TalkData is empty/missing — treat as incomplete
		fmt.Printf("[translate] JP scenario primary returned empty TalkData, trying fallback: %s\n", assetPath)
	} else {
		fmt.Printf("[translate] JP scenario primary failed (%v), trying fallback: %s\n", err, assetPath)
	}

	fallbackURL := fmt.Sprintf("%s/%s.json", jpAssetsFallbackURL, assetPath)
	fallbackResult, fallbackErr := t.fetchJSONURL(fallbackURL)
	if fallbackErr != nil {
		// Both sources failed; if primary at least returned something, use it
		if err == nil && result != nil {
			fmt.Printf("[translate] JP scenario fallback also failed, using primary incomplete data: %s\n", assetPath)
			return result, nil
		}
		return nil, fmt.Errorf("primary: %w; fallback: %v", err, fallbackErr)
	}
	return fallbackResult, nil
}

// scenarioHasTalkData checks whether a scenario JSON contains non-empty TalkData.
func scenarioHasTalkData(data any) bool {
	m, ok := data.(map[string]any)
	if !ok {
		return false
	}
	td, ok := m["TalkData"]
	if !ok {
		return false
	}
	arr, ok := td.([]any)
	return ok && len(arr) > 0
}

func (t *Translator) fetchJSONURLOnce(url string) (any, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 400))
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var raw []byte
	if strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") {
		zr, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, err
		}
		raw, err = io.ReadAll(zr)
		zr.Close()
		if err != nil {
			return nil, err
		}
	} else {
		raw, err = io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
	}
	var parsed any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, err
	}
	return parsed, nil
}

func (t *Translator) extractCards() (map[string]map[string]string, TraceMap, error) {
	jp, err := t.fetchMasterdata("cards.json", "jp")
	if err != nil {
		return nil, nil, err
	}
	cn, err := t.fetchMasterdata("cards.json", "cn")
	if err != nil {
		return nil, nil, err
	}
	cnByID := byIntID(cn, "id")
	out := map[string]map[string]string{"prefix": {}, "skillName": {}, "gachaPhrase": {}}
	trace := newTraceMap("prefix", "skillName", "gachaPhrase")
	for _, item := range jp {
		id := getInt(item, "id")
		cnItem := cnByID[id]
		jpPrefix := getString(item, "prefix")
		addTrace(trace, "prefix", jpPrefix, id)
		collectPair(out["prefix"], jpPrefix, getString(cnItem, "prefix"), true)
		jpSkill := getString(item, "cardSkillName")
		addTrace(trace, "skillName", jpSkill, id)
		collectPair(out["skillName"], jpSkill, getString(cnItem, "cardSkillName"), true)
		phrase := getString(item, "gachaPhrase")
		if phrase != "" && phrase != "-" {
			addTrace(trace, "gachaPhrase", phrase, id)
			collectPair(out["gachaPhrase"], phrase, getString(cnItem, "gachaPhrase"), true)
		}
	}
	return out, trace, nil
}

func (t *Translator) extractEvents() (map[string]map[string]string, TraceMap, error) {
	return t.extractSimpleNameByID("events.json", "id", "name")
}

func (t *Translator) extractGacha() (map[string]map[string]string, TraceMap, error) {
	return t.extractSimpleNameByID("gachas.json", "id", "name")
}

func (t *Translator) extractVirtualLive() (map[string]map[string]string, TraceMap, error) {
	return t.extractSimpleNameByID("virtualLives.json", "id", "name")
}

func (t *Translator) extractStickers() (map[string]map[string]string, TraceMap, error) {
	return t.extractSimpleNameByID("stamps.json", "id", "name")
}

func (t *Translator) extractComics() (map[string]map[string]string, TraceMap, error) {
	jp, err := t.fetchMasterdata("tips.json", "jp")
	if err != nil {
		return nil, nil, err
	}
	cn, err := t.fetchMasterdata("tips.json", "cn")
	if err != nil {
		return nil, nil, err
	}
	cnByID := byIntID(cn, "id")
	out := map[string]map[string]string{"title": {}}
	trace := newTraceMap("title")
	for _, item := range jp {
		if getString(item, "assetbundleName") == "" {
			continue
		}
		id := getInt(item, "id")
		jpTitle := getString(item, "title")
		addTrace(trace, "title", jpTitle, id)
		collectPair(out["title"], jpTitle, getString(cnByID[id], "title"), true)
	}
	return out, trace, nil
}

func (t *Translator) extractMusic() (map[string]map[string]string, TraceMap, error) {
	musics, err := t.fetchMasterdata("musics.json", "jp")
	if err != nil {
		return nil, nil, err
	}
	vocals, _ := t.fetchMasterdata("musicVocals.json", "jp")
	out := map[string]map[string]string{"title": {}, "artist": {}, "vocalCaption": {}}
	trace := newTraceMap("title", "artist", "vocalCaption")
	for _, m := range musics {
		musicID := getInt(m, "id")
		title := getString(m, "title")
		if title != "" {
			out["title"][title] = ""
			addTrace(trace, "title", title, musicID)
		}
		for _, key := range []string{"lyricist", "composer", "arranger"} {
			v := getString(m, key)
			if v != "" && v != "-" {
				out["artist"][v] = ""
				addTrace(trace, "artist", v, musicID)
			}
		}
	}
	for _, v := range vocals {
		vocalID := getInt(v, "id")
		if vocalID == 0 {
			vocalID = getInt(v, "musicId")
		}
		caption := getString(v, "caption")
		if caption != "" {
			out["vocalCaption"][caption] = ""
			addTrace(trace, "vocalCaption", caption, vocalID)
		}
	}
	return out, trace, nil
}

func (t *Translator) extractMysekai() (map[string]map[string]string, TraceMap, error) {
	out := map[string]map[string]string{"fixtureName": {}, "flavorText": {}, "genre": {}, "tag": {}}
	trace := newTraceMap("fixtureName", "flavorText", "genre", "tag")
	jpFix, err := t.fetchMasterdata("mysekaiFixtures.json", "jp")
	if err != nil {
		return nil, nil, err
	}
	cnFix, err := t.fetchMasterdata("mysekaiFixtures.json", "cn")
	if err != nil {
		return nil, nil, err
	}
	cnFixByID := byIntID(cnFix, "id")
	for _, f := range jpFix {
		id := getInt(f, "id")
		cnf := cnFixByID[id]
		jpName := getString(f, "name")
		addTrace(trace, "fixtureName", jpName, id)
		collectPair(out["fixtureName"], jpName, getString(cnf, "name"), true)
		jpFlavor := getString(f, "flavorText")
		addTrace(trace, "flavorText", jpFlavor, id)
		collectPair(out["flavorText"], jpFlavor, getString(cnf, "flavorText"), true)
	}

	jpGenre, _ := t.fetchMasterdata("mysekaiFixtureMainGenres.json", "jp")
	cnGenre, err := t.fetchMasterdata("mysekaiFixtureMainGenres.json", "cn")
	if err != nil {
		return nil, nil, err
	}
	cnGenreByID := byIntID(cnGenre, "id")
	for _, g := range jpGenre {
		id := getInt(g, "id")
		jpName := getString(g, "name")
		addTrace(trace, "genre", jpName, id)
		collectPair(out["genre"], jpName, getString(cnGenreByID[id], "name"), true)
	}

	jpTag, _ := t.fetchMasterdata("mysekaiFixtureTags.json", "jp")
	cnTag, err := t.fetchMasterdata("mysekaiFixtureTags.json", "cn")
	if err != nil {
		return nil, nil, err
	}
	cnTagByID := byIntID(cnTag, "id")
	for _, g := range jpTag {
		id := getInt(g, "id")
		jpName := getString(g, "name")
		addTrace(trace, "tag", jpName, id)
		collectPair(out["tag"], jpName, getString(cnTagByID[id], "name"), true)
	}
	return out, trace, nil
}

func (t *Translator) extractCostumes() (map[string]map[string]string, TraceMap, error) {
	out := map[string]map[string]string{"name": {}, "colorName": {}, "designer": {}}
	trace := newTraceMap("name", "colorName", "designer")
	jpRaw, err := t.fetchJSONURL(jpMasterdataURL + "/snowy_costumes.json")
	if err != nil {
		return nil, nil, err
	}
	cnRaw, err := t.fetchJSONURL(cnMasterdataURL + "/snowy_costumes.json")
	if err != nil {
		return nil, nil, err
	}
	jpMap, _ := jpRaw.(map[string]any)
	cnMap, _ := cnRaw.(map[string]any)
	jpList := toMapSlice(jpMap["costumes"])
	cnList := toMapSlice(cnMap["costumes"])
	cnByID := byIntID(cnList, "id")

	for _, costume := range jpList {
		id := getInt(costume, "id")
		cnCostume := cnByID[id]
		jpName := safeText(getString(costume, "name"))
		addTrace(trace, "name", jpName, id)
		collectPair(out["name"], jpName, safeText(getString(cnCostume, "name")), true)
		jpDesigner := safeText(getString(costume, "designer"))
		addTrace(trace, "designer", jpDesigner, id)
		collectPair(out["designer"], jpDesigner, safeText(getString(cnCostume, "designer")), true)

		jpParts := toParts(costume["parts"])
		cnParts := toParts(cnCostume["parts"])
		for partType, partList := range jpParts {
			cnPartByAsset := map[string]map[string]any{}
			for _, p := range cnParts[partType] {
				cnPartByAsset[getString(p, "assetbundleName")] = p
			}
			for _, p := range partList {
				jpColor := safeText(getString(p, "colorName"))
				if jpColor == "" {
					continue
				}
				addTrace(trace, "colorName", jpColor, id)
				asset := getString(p, "assetbundleName")
				cnColor := safeText(getString(cnPartByAsset[asset], "colorName"))
				collectPair(out["colorName"], jpColor, cnColor, true)
			}
		}
	}
	return out, trace, nil
}

func (t *Translator) extractCharacters() (map[string]map[string]string, TraceMap, error) {
	fields := []string{"hobby", "specialSkill", "favoriteFood", "hatedFood", "weak", "introduction"}
	out := make(map[string]map[string]string, len(fields))
	for _, f := range fields {
		out[f] = map[string]string{}
	}
	trace := newTraceMap(fields...)
	jp, err := t.fetchMasterdata("characterProfiles.json", "jp")
	if err != nil {
		return nil, nil, err
	}
	cn, err := t.fetchMasterdata("characterProfiles.json", "cn")
	if err != nil {
		return nil, nil, err
	}
	cnByID := byIntID(cn, "characterId")
	for _, profile := range jp {
		id := getInt(profile, "characterId")
		cnProfile := cnByID[id]
		for _, field := range fields {
			jpText := safeText(getString(profile, field))
			addTrace(trace, field, jpText, id)
			collectPair(out[field], jpText, safeText(getString(cnProfile, field)), true)
		}
	}
	return out, trace, nil
}

func (t *Translator) extractUnits() (map[string]map[string]string, TraceMap, error) {
	out := map[string]map[string]string{"unitName": {}, "profileSentence": {}}
	trace := newTraceMap("unitName", "profileSentence")
	jp, err := t.fetchMasterdata("unitProfiles.json", "jp")
	if err != nil {
		return nil, nil, err
	}
	cn, err := t.fetchMasterdata("unitProfiles.json", "cn")
	if err != nil {
		return nil, nil, err
	}
	cnByUnit := map[string]map[string]any{}
	for _, unit := range cn {
		cnByUnit[getString(unit, "unit")] = unit
	}
	for _, unit := range jp {
		id := getString(unit, "unit")
		cnUnit := cnByUnit[id]
		jpUnitName := getString(unit, "unitName")
		addTraceStr(trace, "unitName", jpUnitName, id)
		collectPair(out["unitName"], jpUnitName, getString(cnUnit, "unitName"), true)
		jpSentence := getString(unit, "profileSentence")
		addTraceStr(trace, "profileSentence", jpSentence, id)
		collectPair(out["profileSentence"], jpSentence, getString(cnUnit, "profileSentence"), true)
	}
	return out, trace, nil
}

func (t *Translator) extractSimpleNameByID(fileName, idField, nameField string) (map[string]map[string]string, TraceMap, error) {
	jp, err := t.fetchMasterdata(fileName, "jp")
	if err != nil {
		return nil, nil, err
	}
	cn, err := t.fetchMasterdata(fileName, "cn")
	if err != nil {
		return nil, nil, err
	}
	cnByID := byIntID(cn, idField)
	out := map[string]map[string]string{"name": {}}
	trace := newTraceMap("name")
	for _, item := range jp {
		id := getInt(item, idField)
		jpName := getString(item, nameField)
		addTrace(trace, "name", jpName, id)
		collectPair(out["name"], jpName, getString(cnByID[id], nameField), true)
	}
	return out, trace, nil
}

func (t *Translator) syncEventStoriesCNOnly() (int, error) {
	jpStories, err := t.fetchMasterdata("eventStories.json", "jp")
	if err != nil {
		return 0, err
	}
	cnStories, err := t.fetchMasterdata("eventStories.json", "cn")
	if err != nil {
		return 0, err
	}
	cnEvents, err := t.fetchMasterdata("events.json", "cn")
	if err != nil {
		return 0, err
	}
	cnStoryByEvent := byIntID(cnStories, "eventId")
	cnEventSet := map[int]bool{}
	for _, e := range cnEvents {
		cnEventSet[getInt(e, "id")] = true
	}
	sort.Slice(jpStories, func(i, j int) bool {
		return getInt(jpStories[i], "eventId") < getInt(jpStories[j], "eventId")
	})

	dir := filepath.Join(t.store.path, "eventStory")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}

	localStates, localMaxEventID, err := t.loadLocalEventStoryStates(dir)
	if err != nil {
		return 0, err
	}
	latestOfficialCNEvent := 0
	firstLLMEvent := 0
	for _, state := range localStates {
		if state.IsOfficialCN && state.EventID > latestOfficialCNEvent {
			latestOfficialCNEvent = state.EventID
		}
		if state.IsLLM && (firstLLMEvent == 0 || state.EventID < firstLLMEvent) {
			firstLLMEvent = state.EventID
		}
	}

	startCNEventID := 1
	if firstLLMEvent > 0 {
		startCNEventID = firstLLMEvent
	} else if latestOfficialCNEvent > 0 {
		startCNEventID = latestOfficialCNEvent + 1
	}

	fmt.Printf("[translate] cn-sync event stories strategy: startCN=%d latestOfficialCN=%d firstLLM=%d localMax=%d\n",
		startCNEventID, latestOfficialCNEvent, firstLLMEvent, localMaxEventID)

	processed := 0
	scenarioErrors := 0
	emptyCNStreak := 0
	cnStoppedByEmpty := false
	lastCheckedEventID := 0
	for _, jpStory := range jpStories {
		eventID := getInt(jpStory, "eventId")
		if eventID < startCNEventID {
			continue
		}
		lastCheckedEventID = eventID

		if state, ok := localStates[eventID]; ok && (state.IsOfficialCN || state.PreserveLocal) {
			emptyCNStreak = 0
			continue
		}

		if !cnEventSet[eventID] {
			emptyCNStreak++
			fmt.Printf("  [CN] Event %d: CN event not published (%d/3)\n", eventID, emptyCNStreak)
			if emptyCNStreak >= 3 {
				cnStoppedByEmpty = true
				break
			}
			continue
		}
		cnStory := cnStoryByEvent[eventID]
		if cnStory == nil {
			emptyCNStreak++
			fmt.Printf("  [CN] Event %d: CN story metadata missing (%d/3)\n", eventID, emptyCNStreak)
			if emptyCNStreak >= 3 {
				cnStoppedByEmpty = true
				break
			}
			continue
		}

		episodes, hasTalkData, hasTitleOnly, errs := t.buildOfficialCNEpisodes(jpStory, cnStory)
		scenarioErrors += errs
		if errs > 0 {
			fmt.Printf("  [CN] Event %d: scenario fetch failed (%d), skip overwrite this round\n", eventID, errs)
			continue
		}
		if !hasTalkData {
			emptyCNStreak++
			reason := "empty"
			if hasTitleOnly {
				reason = "title-only"
			}
			fmt.Printf("  [CN] Event %d: %s (%d/3)\n", eventID, reason, emptyCNStreak)
			if emptyCNStreak >= 3 {
				cnStoppedByEmpty = true
				break
			}
			continue
		}
		emptyCNStreak = 0

		if err := t.writeEventStoryFile(dir, eventID, "official_cn", episodes); err != nil {
			return processed, err
		}
		localStates[eventID] = localEventStoryState{EventID: eventID, Source: "official_cn", IsOfficialCN: true}
		if eventID > localMaxEventID {
			localMaxEventID = eventID
		}
		processed++
	}

	if cnStoppedByEmpty {
		fallbackStart := localMaxEventID + 1
		if fallbackStart > 0 {
			fmt.Printf("[translate] cn-sync event stories: CN empty streak reached at event %d, fallback JP-only from event %d\n", lastCheckedEventID, fallbackStart)
			fallbackProcessed, fallbackErrors, err := t.fillEventStoriesJPPending(jpStories, fallbackStart, dir, localStates)
			if err != nil {
				return processed, err
			}
			processed += fallbackProcessed
			scenarioErrors += fallbackErrors
		}
	}

	if scenarioErrors > 0 {
		fmt.Printf("[translate] cn-sync event stories warning: scenario fetch failures=%d\n", scenarioErrors)
	}

	return processed, nil
}

func (t *Translator) loadLocalEventStoryStates(dir string) (map[int]localEventStoryState, int, error) {
	states := map[int]localEventStoryState{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return states, 0, nil
		}
		return nil, 0, err
	}
	maxID := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "event_") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		idText := strings.TrimSuffix(strings.TrimPrefix(entry.Name(), "event_"), ".json")
		eventID, err := strconv.Atoi(idText)
		if err != nil || eventID <= 0 {
			continue
		}
		if eventID > maxID {
			maxID = eventID
		}

		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		state := localEventStoryState{EventID: eventID}
		fullPath := eventStoryFullJSONPath(dir, eventID)
		detail, err := parseEventStoryDetail(raw, info.ModTime().Unix())
		if err != nil {
			state.Source = "unknown"
		} else {
			state.Source = detail.Meta.Source
			if detail.Meta.Version == "legacy" && detail.Meta.Source == "official_cn" {
				state.Source = "official_cn_legacy"
			}
			state.IsOfficialCN = detail.Meta.Source == "official_cn"
			state.IsLLM = detail.Meta.Source == "llm"
			if detail.Meta.Source == SourceHuman || detail.Meta.Source == SourcePinned {
				state.PreserveLocal = true
			}
		}
		fullRaw, fullErr := os.ReadFile(fullPath)
		if fullErr == nil {
			state.HasCompanion = true
			fullInfo, statErr := os.Stat(fullPath)
			if statErr != nil {
				state.PreserveLocal = true
			} else {
				fullDetail, parseErr := parseEventStoryFullDetail(fullRaw, fullInfo.ModTime().Unix(), detail)
				if parseErr != nil {
					state.PreserveLocal = true
				} else if eventStoryFullHasEditableSources(fullDetail) {
					state.PreserveLocal = true
				}
			}
		}
		states[eventID] = state
	}
	return states, maxID, nil
}

func (t *Translator) buildOfficialCNEpisodes(jpStory, cnStory map[string]any) (map[string]eventStoryEpisodePayload, bool, bool, int) {
	asset := getString(jpStory, "assetbundleName")
	jpEpisodes := toMapSlice(jpStory["eventStoryEpisodes"])
	cnEpisodes := toMapSlice(cnStory["eventStoryEpisodes"])
	cnByEp := byIntID(cnEpisodes, "episodeNo")

	episodes := map[string]eventStoryEpisodePayload{}
	hasTalkData := false
	hasTitleOnly := false
	errs := 0

	for _, ep := range jpEpisodes {
		epNo := getInt(ep, "episodeNo")
		scenarioID := getString(ep, "scenarioId")
		if scenarioID == "" {
			continue
		}
		scenarioPath := fmt.Sprintf("event_story/%s/scenario/%s", asset, scenarioID)
		jpScenario, err := t.fetchJPScenarioJSON(scenarioPath)
		if err != nil {
			errs++
			continue
		}
		cnScenario, err := t.fetchJSONURL(fmt.Sprintf("%s/%s.json", cnAssetsURL, scenarioPath))
		if err != nil {
			errs++
			continue
		}

		jpTalk := toMapSlice(asMap(jpScenario)["TalkData"])
		cnTalk := toMapSlice(asMap(cnScenario)["TalkData"])
		talkData := map[string]string{}
		for i := 0; i < len(jpTalk) && i < len(cnTalk); i++ {
			jpBody := strings.TrimSpace(getString(jpTalk[i], "Body"))
			cnBody := strings.TrimSpace(getString(cnTalk[i], "Body"))
			if jpBody != "" && cnBody != "" && jpBody != cnBody {
				talkData[jpBody] = cnBody
			}
			jpName := strings.TrimSpace(getString(jpTalk[i], "WindowDisplayName"))
			cnName := strings.TrimSpace(getString(cnTalk[i], "WindowDisplayName"))
			if jpName != "" && cnName != "" && jpName != cnName {
				talkData[jpName] = cnName
			}
		}

		cnTitle := strings.TrimSpace(getString(cnByEp[epNo], "title"))
		jpTitle := strings.TrimSpace(getString(ep, "title"))
		if cnTitle == jpTitle {
			cnTitle = ""
		}

		if len(talkData) > 0 {
			hasTalkData = true
		} else if cnTitle != "" {
			hasTitleOnly = true
		}

		if len(talkData) == 0 && cnTitle == "" {
			continue
		}

		episodes[strconv.Itoa(epNo)] = eventStoryEpisodePayload{
			ScenarioID: scenarioID,
			Title:      cnTitle,
			TalkData:   talkData,
		}
	}

	return episodes, hasTalkData, hasTitleOnly, errs
}

func (t *Translator) fillEventStoriesJPPending(jpStories []map[string]any, startEventID int, dir string, localStates map[int]localEventStoryState) (int, int, error) {
	processed := 0
	scenarioErrors := 0
	for _, jpStory := range jpStories {
		eventID := getInt(jpStory, "eventId")
		if eventID < startEventID {
			continue
		}
		if state, exists := localStates[eventID]; exists {
			if state.Source == "jp_pending" && !state.PreserveLocal {
				translated, autoErr := t.autoTranslateEventStory(dir, eventID)
				if autoErr != nil {
					fmt.Printf("[translate] auto-ai event story retry pending for event %d: %v\n", eventID, autoErr)
				} else if translated > 0 {
					state.Source = SourceLLM
					state.IsLLM = true
					state.HasCompanion = true
					localStates[eventID] = state
					processed++
					fmt.Printf("[translate] auto-ai event story retry completed for event %d: translated=%d\n", eventID, translated)
				}
			}
			continue
		}

		episodes, errs := t.buildJPPendingEpisodes(jpStory)
		scenarioErrors += errs
		if len(episodes) == 0 {
			continue
		}

		if err := t.writeEventStoryFile(dir, eventID, "jp_pending", episodes); err != nil {
			return processed, scenarioErrors, err
		}
		state := localEventStoryState{EventID: eventID, Source: "jp_pending"}
		translated, autoErr := t.autoTranslateEventStory(dir, eventID)
		if autoErr != nil {
			fmt.Printf("[translate] auto-ai event story pending retry for event %d: %v\n", eventID, autoErr)
		} else if translated > 0 {
			state.Source = SourceLLM
			state.IsLLM = true
			state.HasCompanion = true
			fmt.Printf("[translate] auto-ai event story completed for event %d: translated=%d\n", eventID, translated)
		}
		localStates[eventID] = state
		processed++
	}
	return processed, scenarioErrors, nil
}

func (t *Translator) autoTranslateEventStory(dir string, eventID int) (int, error) {
	provider := strings.ToLower(strings.TrimSpace(t.cfg.LLMType))
	if provider == "" {
		provider = "gemini"
	}
	if provider != "gemini" && provider != "openai" {
		return 0, fmt.Errorf("unsupported provider: %s", provider)
	}

	path := eventStoryJSONPath(dir, eventID)
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	detail, err := parseEventStoryDetail(data, info.ModTime().Unix())
	if err != nil {
		return 0, err
	}
	if normalizeEventStoryStorySource(detail.Meta.Source) != "jp_pending" {
		return 0, nil
	}
	fullDetail, err := t.loadOrBuildEventStoryFullDetail(dir, eventID, detail)
	if err != nil {
		return 0, err
	}

	targets := collectEventStoryTranslateTargets(detail, fullDetail)
	if len(targets) == 0 {
		return 0, nil
	}

	texts := make([]string, 0, len(targets))
	for _, target := range targets {
		texts = append(texts, target.JP)
	}
	translated, err := t.callLLM(provider, texts)
	if err != nil {
		return 0, err
	}

	translatedCount := 0
	for idx, target := range targets {
		if idx >= len(translated) {
			break
		}
		cn := strings.TrimSpace(translated[idx])
		if cn == "" {
			continue
		}
		episode := detail.Episodes[target.EpisodeNo]
		fullEpisode := fullDetail.Episodes[target.EpisodeNo]
		switch target.EntryType {
		case "title":
			episode.Title = cn
			fullEpisode.Title.Text = cn
			fullEpisode.Title.Source = SourceLLM
		case "talk":
			episode.TalkData[target.JP] = cn
			line := fullEpisode.TalkData[target.JP]
			line.Text = cn
			line.Source = SourceLLM
			fullEpisode.TalkData[target.JP] = line
		default:
			continue
		}
		detail.Episodes[target.EpisodeNo] = episode
		fullDetail.Episodes[target.EpisodeNo] = fullEpisode
		translatedCount++
	}
	if translatedCount == 0 {
		return 0, nil
	}

	now := time.Now().Unix()
	storySource := "jp_pending"
	if translatedCount == len(targets) {
		storySource = SourceLLM
	}
	detail.Meta.Source = storySource
	detail.Meta.Version = "1.0"
	detail.Meta.LastUpdated = now
	fullDetail.Meta.Source = storySource
	fullDetail.Meta.Version = "1.0"
	fullDetail.Meta.LastUpdated = now
	if err := writeEventStoryDetailFiles(dir, eventID, detail, fullDetail); err != nil {
		return 0, err
	}
	return translatedCount, nil
}

func (t *Translator) buildJPPendingEpisodes(jpStory map[string]any) (map[string]eventStoryEpisodePayload, int) {
	asset := getString(jpStory, "assetbundleName")
	jpEpisodes := toMapSlice(jpStory["eventStoryEpisodes"])
	episodes := map[string]eventStoryEpisodePayload{}
	errs := 0

	for _, ep := range jpEpisodes {
		epNo := getInt(ep, "episodeNo")
		scenarioID := getString(ep, "scenarioId")
		if scenarioID == "" {
			continue
		}
		title := strings.TrimSpace(getString(ep, "title"))
		scenarioPath := fmt.Sprintf("event_story/%s/scenario/%s", asset, scenarioID)
		jpScenario, err := t.fetchJPScenarioJSON(scenarioPath)
		if err != nil {
			errs++
			if title != "" {
				episodes[strconv.Itoa(epNo)] = eventStoryEpisodePayload{
					ScenarioID: scenarioID,
					Title:      title,
					TalkData:   map[string]string{},
				}
			}
			continue
		}

		jpTalk := toMapSlice(asMap(jpScenario)["TalkData"])
		talkData := map[string]string{}
		for _, talk := range jpTalk {
			jpBody := strings.TrimSpace(getString(talk, "Body"))
			if jpBody != "" {
				talkData[jpBody] = ""
			}
			jpName := strings.TrimSpace(getString(talk, "WindowDisplayName"))
			if jpName != "" {
				talkData[jpName] = ""
			}
		}
		if len(talkData) == 0 && title == "" {
			continue
		}
		episodes[strconv.Itoa(epNo)] = eventStoryEpisodePayload{
			ScenarioID: scenarioID,
			Title:      title,
			TalkData:   talkData,
		}
	}

	return episodes, errs
}

func (t *Translator) writeEventStoryFile(dir string, eventID int, source string, episodes map[string]eventStoryEpisodePayload) error {
	payload := map[string]any{
		"meta": map[string]any{
			"source":       source,
			"version":      "1.0",
			"last_updated": time.Now().Unix(),
		},
		"episodes": episodes,
	}
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	outPath := filepath.Join(dir, fmt.Sprintf("event_%d.json", eventID))
	return writeAtomic(outPath, b)
}

func writeEventStoryDetailFiles(dir string, eventID int, detail EventStoryDetail, fullDetail eventStoryFullDetail) error {
	mainPath := eventStoryJSONPath(dir, eventID)
	fullPath := eventStoryFullJSONPath(dir, eventID)
	originalMainData, err := os.ReadFile(mainPath)
	if err != nil {
		return err
	}
	originalFullData, originalFullErr := os.ReadFile(fullPath)
	fullExisted := originalFullErr == nil

	mainOut, err := json.MarshalIndent(detail, "", "  ")
	if err != nil {
		return err
	}
	if err := writeAtomic(mainPath, mainOut); err != nil {
		return err
	}
	fullOut, err := json.MarshalIndent(fullDetail, "", "  ")
	if err != nil {
		_ = writeAtomic(mainPath, originalMainData)
		return err
	}
	if err := writeAtomic(fullPath, fullOut); err != nil {
		rollbackErr := writeAtomic(mainPath, originalMainData)
		if rollbackErr != nil {
			return fmt.Errorf("write event story full sidecar: %w (rollback failed: %v)", err, rollbackErr)
		}
		if fullExisted {
			_ = writeAtomic(fullPath, originalFullData)
		}
		return err
	}
	return nil
}

// RetryEventStorySync re-fetches and rebuilds the event story for a single event.
// It tries CN official data first; if unavailable, falls back to JP pending + auto LLM translate.
// Unlike the normal sync, this ignores the local state and always overwrites.
func (t *Translator) RetryEventStorySync(eventID int) (map[string]any, error) {
	if err := t.markStart("retry-event-story"); err != nil {
		return nil, err
	}
	var runErr error
	defer func() {
		note := fmt.Sprintf("retry event story %d complete", eventID)
		if runErr != nil {
			note = fmt.Sprintf("retry event story %d failed", eventID)
		}
		t.markEnd(note, runErr)
	}()

	result, err := t.retryEventStorySyncInner(eventID)
	if err != nil {
		runErr = err
	}
	return result, err
}

func (t *Translator) retryEventStorySyncInner(eventID int) (map[string]any, error) {
	dir := filepath.Join(t.store.path, "eventStory")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	// Fetch masterdata
	jpStories, err := t.fetchMasterdata("eventStories.json", "jp")
	if err != nil {
		return nil, fmt.Errorf("fetch JP eventStories: %w", err)
	}
	cnStories, err := t.fetchMasterdata("eventStories.json", "cn")
	if err != nil {
		return nil, fmt.Errorf("fetch CN eventStories: %w", err)
	}
	cnEvents, err := t.fetchMasterdata("events.json", "cn")
	if err != nil {
		return nil, fmt.Errorf("fetch CN events: %w", err)
	}

	// Find the JP story for this event
	var jpStory map[string]any
	for _, s := range jpStories {
		if getInt(s, "eventId") == eventID {
			jpStory = s
			break
		}
	}
	if jpStory == nil {
		return nil, fmt.Errorf("event %d not found in JP eventStories", eventID)
	}

	cnStoryByEvent := byIntID(cnStories, "eventId")
	cnEventSet := map[int]bool{}
	for _, e := range cnEvents {
		cnEventSet[getInt(e, "id")] = true
	}

	result := map[string]any{"eventId": eventID}

	// Try CN official first
	if cnEventSet[eventID] {
		if cnStory := cnStoryByEvent[eventID]; cnStory != nil {
			episodes, hasTalkData, _, errs := t.buildOfficialCNEpisodes(jpStory, cnStory)
			if errs == 0 && hasTalkData {
				if err := t.writeEventStoryFile(dir, eventID, "official_cn", episodes); err != nil {
					return nil, err
				}
				result["source"] = "official_cn"
				result["episodes"] = len(episodes)
				fmt.Printf("[retry-event-story] event %d: wrote official_cn (%d episodes)\n", eventID, len(episodes))
				return result, nil
			}
			if errs > 0 {
				fmt.Printf("[retry-event-story] event %d: CN scenario fetch had %d errors, falling back to JP\n", eventID, errs)
			}
		}
	}

	// Fallback: JP pending + auto LLM translate
	episodes, errs := t.buildJPPendingEpisodes(jpStory)
	if len(episodes) == 0 {
		return nil, fmt.Errorf("event %d: no episodes could be fetched (errors=%d)", eventID, errs)
	}

	if err := t.writeEventStoryFile(dir, eventID, "jp_pending", episodes); err != nil {
		return nil, err
	}
	result["source"] = "jp_pending"
	result["episodes"] = len(episodes)
	result["fetchErrors"] = errs

	// Try auto LLM translate
	translated, autoErr := t.autoTranslateEventStory(dir, eventID)
	if autoErr != nil {
		fmt.Printf("[retry-event-story] event %d: auto-translate failed: %v\n", eventID, autoErr)
		result["translateError"] = autoErr.Error()
	} else if translated > 0 {
		result["source"] = "llm"
		result["translated"] = translated
		fmt.Printf("[retry-event-story] event %d: auto-translated %d lines\n", eventID, translated)
	}

	fmt.Printf("[retry-event-story] event %d: wrote %s (%d episodes, %d fetch errors)\n",
		eventID, result["source"], len(episodes), errs)
	return result, nil
}

func collectEventStoryTranslateTargets(detail EventStoryDetail, fullDetail eventStoryFullDetail) []eventStoryTranslateTarget {
	episodeNos := make([]string, 0, len(detail.Episodes))
	for episodeNo := range detail.Episodes {
		episodeNos = append(episodeNos, episodeNo)
	}
	sort.Slice(episodeNos, func(i, j int) bool {
		left, leftErr := strconv.Atoi(episodeNos[i])
		right, rightErr := strconv.Atoi(episodeNos[j])
		if leftErr == nil && rightErr == nil {
			return left < right
		}
		return episodeNos[i] < episodeNos[j]
	})

	targets := make([]eventStoryTranslateTarget, 0)
	for _, episodeNo := range episodeNos {
		episode := detail.Episodes[episodeNo]
		fullEpisode := fullDetail.Episodes[episodeNo]
		titleSource := normalizeEventStoryLineSource(fullEpisode.Title.Source)
		if titleSource != SourceHuman && titleSource != SourcePinned && titleSource != SourceCN {
			titleJP := strings.TrimSpace(episode.Title)
			if titleJP != "" && !(titleSource == SourceLLM && strings.TrimSpace(fullEpisode.Title.Text) != "") {
				targets = append(targets, eventStoryTranslateTarget{EpisodeNo: episodeNo, EntryType: "title", JP: titleJP})
			}
		}

		keys := make([]string, 0, len(episode.TalkData))
		for jp := range episode.TalkData {
			keys = append(keys, jp)
		}
		sort.Strings(keys)
		for _, jp := range keys {
			line := fullEpisode.TalkData[jp]
			source := normalizeEventStoryLineSource(line.Source)
			if source == SourceHuman || source == SourcePinned || source == SourceCN {
				continue
			}
			if source == SourceLLM && strings.TrimSpace(line.Text) != "" {
				continue
			}
			targets = append(targets, eventStoryTranslateTarget{EpisodeNo: episodeNo, EntryType: "talk", JP: jp})
		}
	}
	return targets
}

func (t *Translator) callLLM(provider string, texts []string) ([]string, error) {
	if len(texts) == 0 {
		return []string{}, nil
	}
	prompt := gameContextPrompt + buildXMLInput(texts)
	for attempt := 1; attempt <= 3; attempt++ {
		var content string
		var err error
		switch provider {
		case "gemini":
			content, err = t.callGemini(prompt)
		case "openai":
			content, err = t.callOpenAI(prompt)
		default:
			return nil, fmt.Errorf("unsupported provider: %s", provider)
		}
		if err == nil {
			parsed := parseXMLTranslations(content, len(texts))
			nonEmpty := 0
			for _, s := range parsed {
				if strings.TrimSpace(s) != "" {
					nonEmpty++
				}
			}
			if len(parsed) == len(texts) || nonEmpty >= len(texts)/2 {
				return parsed, nil
			}
		}
		time.Sleep(time.Duration(attempt) * time.Second)
	}
	return nil, fmt.Errorf("llm translation failed after retries")
}

func (t *Translator) callGemini(prompt string) (string, error) {
	if strings.TrimSpace(t.cfg.GeminiAPIKey) == "" {
		return "", fmt.Errorf("GEMINI_API_KEY is not configured")
	}
	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent", t.cfg.GeminiModel)
	payload := map[string]any{
		"contents": []map[string]any{{"parts": []map[string]string{{"text": prompt}}}},
		"generationConfig": map[string]any{
			"temperature":      0.3,
			"maxOutputTokens":  8192,
			"candidateCount":   1,
			"responseMimeType": "text/plain",
		},
	}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", t.cfg.GeminiAPIKey)
	resp, err := t.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gemini http %d: %s", resp.StatusCode, string(raw))
	}
	var result struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", err
	}
	if len(result.Candidates) == 0 || len(result.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("gemini returned empty candidates")
	}
	return result.Candidates[0].Content.Parts[0].Text, nil
}

func (t *Translator) callOpenAI(prompt string) (string, error) {
	if strings.TrimSpace(t.cfg.OpenAIAPIKey) == "" {
		return "", fmt.Errorf("OPENAI_API_KEY is not configured")
	}
	base := strings.TrimRight(t.cfg.OpenAIBaseURL, "/")
	url := base + "/chat/completions"
	payload := map[string]any{
		"model":       t.cfg.OpenAIModel,
		"messages":    []map[string]string{{"role": "user", "content": prompt}},
		"temperature": 0.3,
	}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+t.cfg.OpenAIAPIKey)
	resp, err := t.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("openai http %d: %s", resp.StatusCode, string(raw))
	}
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", err
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("openai returned empty choices")
	}
	return result.Choices[0].Message.Content, nil
}

func buildXMLInput(texts []string) string {
	var b strings.Builder
	for i, s := range texts {
		escaped := xmlEscape(s)
		b.WriteString("<item id=\"")
		b.WriteString(strconv.Itoa(i + 1))
		b.WriteString("\">")
		b.WriteString(escaped)
		b.WriteString("</item>\n")
	}
	return b.String()
}

func parseXMLTranslations(content string, expected int) []string {
	content = regexp.MustCompile(`(?s)<think>.*?</think>`).ReplaceAllString(content, "")
	re := regexp.MustCompile(`(?s)<t\s+id="(\d+)">(.*?)</t>`)
	matches := re.FindAllStringSubmatch(content, -1)
	out := make([]string, expected)
	for _, m := range matches {
		id, err := strconv.Atoi(m[1])
		if err != nil || id <= 0 || id > expected {
			continue
		}
		out[id-1] = xmlUnescape(strings.TrimSpace(m[2]))
	}
	return out
}

func xmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

func xmlUnescape(s string) string {
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&amp;", "&")
	return s
}

func collectPair(target map[string]string, jp, cn string, compare bool) {
	jp = strings.TrimSpace(jp)
	cn = strings.TrimSpace(cn)
	if jp == "" {
		return
	}
	if compare && cn == jp {
		cn = ""
	}
	target[jp] = cn
}

func getString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	s, ok := v.(string)
	if ok {
		return s
	}
	return ""
}

func getInt(m map[string]any, key string) int {
	if m == nil {
		return 0
	}
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case string:
		i, _ := strconv.Atoi(n)
		return i
	default:
		return 0
	}
}

func byIntID(list []map[string]any, key string) map[int]map[string]any {
	out := make(map[int]map[string]any, len(list))
	for _, item := range list {
		id := getInt(item, key)
		if id != 0 {
			out[id] = item
		}
	}
	return out
}

func toMapSlice(v any) []map[string]any {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(arr))
	for _, item := range arr {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func safeText(s string) string {
	s = strings.TrimSpace(s)
	if s == "-" {
		return ""
	}
	return s
}

func toParts(v any) map[string][]map[string]any {
	res := map[string][]map[string]any{}
	m, ok := v.(map[string]any)
	if !ok {
		return res
	}
	for k, raw := range m {
		res[k] = toMapSlice(raw)
	}
	return res
}

// ============================================================================
// TraceMap — tracks which masterdata IDs reference each JP text
// Mirrors translate.py's _add_trace / _merge_trace_ids logic
// ============================================================================

// TraceMap: field -> jpText -> []refID
type TraceMap map[string]map[string][]string

func newTraceMap(fields ...string) TraceMap {
	tm := make(TraceMap, len(fields))
	for _, f := range fields {
		tm[f] = map[string][]string{}
	}
	return tm
}

func addTrace(trace TraceMap, field, jpText string, refID int) {
	jpText = strings.TrimSpace(jpText)
	if jpText == "" || refID == 0 {
		return
	}
	if trace[field] == nil {
		trace[field] = map[string][]string{}
	}
	ref := strconv.Itoa(refID)
	for _, existing := range trace[field][jpText] {
		if existing == ref {
			return
		}
	}
	trace[field][jpText] = append(trace[field][jpText], ref)
}

func addTraceStr(trace TraceMap, field, jpText, refID string) {
	jpText = strings.TrimSpace(jpText)
	refID = strings.TrimSpace(refID)
	if jpText == "" || refID == "" {
		return
	}
	if trace[field] == nil {
		trace[field] = map[string][]string{}
	}
	for _, existing := range trace[field][jpText] {
		if existing == refID {
			return
		}
	}
	trace[field][jpText] = append(trace[field][jpText], refID)
}

func mergeTraceIDs(entry *TranslationEntry, ids []string) {
	if len(ids) == 0 {
		return
	}
	existing := make(map[string]bool, len(entry.Ids))
	for _, id := range entry.Ids {
		existing[id] = true
	}
	for _, id := range ids {
		if !existing[id] {
			entry.Ids = append(entry.Ids, id)
			existing[id] = true
		}
	}
}
