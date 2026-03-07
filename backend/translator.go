package backend

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
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
	jpMasterdataURL = "https://sekaimaster.exmeaning.com/master"
	cnMasterdataURL = "https://sekaimaster-cn.exmeaning.com/master"
	jpAssetsURL     = "https://snowyassets.exmeaning.com/ondemand"
	cnAssetsURL     = "https://sekai-assets-bdf29c81.seiunx.net/cn-assets/ondemand"
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
	Mode            string `json:"mode"`
	Categories      int    `json:"categories"`
	UpdatedEntries  int    `json:"updatedEntries"`
	EventStoryFiles int    `json:"eventStoryFiles"`
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

type EventStoryDetail struct {
	Meta struct {
		Source      string `json:"source"`
		Version     string `json:"version"`
		LastUpdated int64  `json:"last_updated"`
	} `json:"meta"`
	Episodes map[string]struct {
		ScenarioID string            `json:"scenarioId"`
		Title      string            `json:"title"`
		TalkData   map[string]string `json:"talkData"`
	} `json:"episodes"`
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
		fn       func() (map[string]map[string]string, error)
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
		fields, err := item.fn()
		if err != nil {
			runErr = fmt.Errorf("%s: %w", item.category, err)
			return result, runErr
		}
		updated, err := t.applyCategoryCNOnly(item.category, fields)
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
		runErr = err
		return result, runErr
	}
	result.EventStoryFiles = storyCount
	fmt.Printf("[translate] cn-sync event stories completed, files=%d\n", storyCount)
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
		cat[req.Field][jp] = TranslationEntry{Text: cn, Source: SourceLLM}
		result.Translated++
	}
	err := t.saveCategoryLocked(req.Category, cat)
	t.store.mu.Unlock()
	if err != nil {
		runErr = err
		return result, runErr
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
		var detail EventStoryDetail
		if err := json.Unmarshal(data, &detail); err != nil {
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
	path := filepath.Join(t.store.path, "eventStory", fmt.Sprintf("event_%d.json", eventID))
	data, err := os.ReadFile(path)
	if err != nil {
		return detail, err
	}
	if err := json.Unmarshal(data, &detail); err != nil {
		return detail, err
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
		cat[req.Field][jp] = TranslationEntry{Text: cn, Source: SourceLLM}
		result.Translated++
	}
	err := t.saveCategoryLocked(req.Category, cat)
	t.store.mu.Unlock()
	if err != nil {
		return result, err
	}

	return result, nil
}

// UpdateEventStoryLine updates a single line in an event story file.
func (t *Translator) UpdateEventStoryLine(eventID int, episodeNo, jpKey, cnText string) error {
	path := filepath.Join(t.store.path, "eventStory", fmt.Sprintf("event_%d.json", eventID))
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var detail EventStoryDetail
	if err := json.Unmarshal(data, &detail); err != nil {
		return err
	}

	ep, ok := detail.Episodes[episodeNo]
	if !ok {
		return fmt.Errorf("episode %s not found in event %d", episodeNo, eventID)
	}
	if _, exists := ep.TalkData[jpKey]; !exists {
		return fmt.Errorf("key not found in episode %s", episodeNo)
	}
	ep.TalkData[jpKey] = cnText
	detail.Episodes[episodeNo] = ep
	detail.Meta.LastUpdated = time.Now().Unix()

	out, err := json.MarshalIndent(detail, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, out)
}

func (t *Translator) applyCategoryCNOnly(category string, fields map[string]map[string]string) (int, error) {
	t.store.mu.Lock()
	defer t.store.mu.Unlock()

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
					continue
				}
				next = TranslationEntry{Text: cn, Source: SourceCN}
			} else {
				if has && old.Text != "" {
					continue
				}
				next = TranslationEntry{Text: "", Source: SourceUnknown}
			}
			if !has || old.Text != next.Text || old.Source != next.Source {
				cat[field][jp] = next
				updated++
			}
		}
	}

	if err := t.saveCategoryLocked(category, cat); err != nil {
		return updated, err
	}
	return updated, nil
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

func (t *Translator) fetchJSONURL(url string) (any, error) {
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

func (t *Translator) extractCards() (map[string]map[string]string, error) {
	jp, err := t.fetchMasterdata("cards.json", "jp")
	if err != nil {
		return nil, err
	}
	cn, err := t.fetchMasterdata("cards.json", "cn")
	if err != nil {
		return nil, err
	}
	cnByID := byIntID(cn, "id")
	out := map[string]map[string]string{"prefix": {}, "skillName": {}, "gachaPhrase": {}}
	for _, item := range jp {
		id := getInt(item, "id")
		cnItem := cnByID[id]
		collectPair(out["prefix"], getString(item, "prefix"), getString(cnItem, "prefix"), true)
		collectPair(out["skillName"], getString(item, "cardSkillName"), getString(cnItem, "cardSkillName"), true)
		phrase := getString(item, "gachaPhrase")
		if phrase != "" && phrase != "-" {
			collectPair(out["gachaPhrase"], phrase, getString(cnItem, "gachaPhrase"), true)
		}
	}
	return out, nil
}

func (t *Translator) extractEvents() (map[string]map[string]string, error) {
	return t.extractSimpleNameByID("events.json", "id", "name")
}

func (t *Translator) extractGacha() (map[string]map[string]string, error) {
	return t.extractSimpleNameByID("gachas.json", "id", "name")
}

func (t *Translator) extractVirtualLive() (map[string]map[string]string, error) {
	return t.extractSimpleNameByID("virtualLives.json", "id", "name")
}

func (t *Translator) extractStickers() (map[string]map[string]string, error) {
	return t.extractSimpleNameByID("stamps.json", "id", "name")
}

func (t *Translator) extractComics() (map[string]map[string]string, error) {
	jp, err := t.fetchMasterdata("tips.json", "jp")
	if err != nil {
		return nil, err
	}
	cn, err := t.fetchMasterdata("tips.json", "cn")
	if err != nil {
		return nil, err
	}
	cnByID := byIntID(cn, "id")
	out := map[string]map[string]string{"title": {}}
	for _, item := range jp {
		if getString(item, "assetbundleName") == "" {
			continue
		}
		id := getInt(item, "id")
		collectPair(out["title"], getString(item, "title"), getString(cnByID[id], "title"), true)
	}
	return out, nil
}

func (t *Translator) extractMusic() (map[string]map[string]string, error) {
	musics, err := t.fetchMasterdata("musics.json", "jp")
	if err != nil {
		return nil, err
	}
	vocals, _ := t.fetchMasterdata("musicVocals.json", "jp")
	out := map[string]map[string]string{"title": {}, "artist": {}, "vocalCaption": {}}
	for _, m := range musics {
		title := getString(m, "title")
		if title != "" {
			out["title"][title] = ""
		}
		for _, key := range []string{"lyricist", "composer", "arranger"} {
			v := getString(m, key)
			if v != "" && v != "-" {
				out["artist"][v] = ""
			}
		}
	}
	for _, v := range vocals {
		caption := getString(v, "caption")
		if caption != "" {
			out["vocalCaption"][caption] = ""
		}
	}
	return out, nil
}

func (t *Translator) extractMysekai() (map[string]map[string]string, error) {
	out := map[string]map[string]string{"fixtureName": {}, "flavorText": {}, "genre": {}, "tag": {}}
	jpFix, err := t.fetchMasterdata("mysekaiFixtures.json", "jp")
	if err != nil {
		return nil, err
	}
	cnFix, err := t.fetchMasterdata("mysekaiFixtures.json", "cn")
	if err != nil {
		return nil, err
	}
	cnFixByID := byIntID(cnFix, "id")
	for _, f := range jpFix {
		id := getInt(f, "id")
		cnf := cnFixByID[id]
		collectPair(out["fixtureName"], getString(f, "name"), getString(cnf, "name"), true)
		collectPair(out["flavorText"], getString(f, "flavorText"), getString(cnf, "flavorText"), true)
	}

	jpGenre, _ := t.fetchMasterdata("mysekaiFixtureMainGenres.json", "jp")
	cnGenre, err := t.fetchMasterdata("mysekaiFixtureMainGenres.json", "cn")
	if err != nil {
		return nil, err
	}
	cnGenreByID := byIntID(cnGenre, "id")
	for _, g := range jpGenre {
		id := getInt(g, "id")
		collectPair(out["genre"], getString(g, "name"), getString(cnGenreByID[id], "name"), true)
	}

	jpTag, _ := t.fetchMasterdata("mysekaiFixtureTags.json", "jp")
	cnTag, err := t.fetchMasterdata("mysekaiFixtureTags.json", "cn")
	if err != nil {
		return nil, err
	}
	cnTagByID := byIntID(cnTag, "id")
	for _, g := range jpTag {
		id := getInt(g, "id")
		collectPair(out["tag"], getString(g, "name"), getString(cnTagByID[id], "name"), true)
	}
	return out, nil
}

func (t *Translator) extractCostumes() (map[string]map[string]string, error) {
	out := map[string]map[string]string{"name": {}, "colorName": {}, "designer": {}}
	jpRaw, err := t.fetchJSONURL(jpMasterdataURL + "/snowy_costumes.json")
	if err != nil {
		return nil, err
	}
	cnRaw, err := t.fetchJSONURL(cnMasterdataURL + "/snowy_costumes.json")
	if err != nil {
		return nil, err
	}
	jpMap, _ := jpRaw.(map[string]any)
	cnMap, _ := cnRaw.(map[string]any)
	jpList := toMapSlice(jpMap["costumes"])
	cnList := toMapSlice(cnMap["costumes"])
	cnByID := byIntID(cnList, "id")

	for _, costume := range jpList {
		id := getInt(costume, "id")
		cnCostume := cnByID[id]
		collectPair(out["name"], safeText(getString(costume, "name")), safeText(getString(cnCostume, "name")), true)
		collectPair(out["designer"], safeText(getString(costume, "designer")), safeText(getString(cnCostume, "designer")), true)

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
				asset := getString(p, "assetbundleName")
				cnColor := safeText(getString(cnPartByAsset[asset], "colorName"))
				collectPair(out["colorName"], jpColor, cnColor, true)
			}
		}
	}
	return out, nil
}

func (t *Translator) extractCharacters() (map[string]map[string]string, error) {
	fields := []string{"hobby", "specialSkill", "favoriteFood", "hatedFood", "weak", "introduction"}
	out := make(map[string]map[string]string, len(fields))
	for _, f := range fields {
		out[f] = map[string]string{}
	}
	jp, err := t.fetchMasterdata("characterProfiles.json", "jp")
	if err != nil {
		return nil, err
	}
	cn, err := t.fetchMasterdata("characterProfiles.json", "cn")
	if err != nil {
		return nil, err
	}
	cnByID := byIntID(cn, "characterId")
	for _, profile := range jp {
		id := getInt(profile, "characterId")
		cnProfile := cnByID[id]
		for _, field := range fields {
			collectPair(out[field], safeText(getString(profile, field)), safeText(getString(cnProfile, field)), true)
		}
	}
	return out, nil
}

func (t *Translator) extractUnits() (map[string]map[string]string, error) {
	out := map[string]map[string]string{"unitName": {}, "profileSentence": {}}
	jp, err := t.fetchMasterdata("unitProfiles.json", "jp")
	if err != nil {
		return nil, err
	}
	cn, err := t.fetchMasterdata("unitProfiles.json", "cn")
	if err != nil {
		return nil, err
	}
	cnByUnit := map[string]map[string]any{}
	for _, unit := range cn {
		cnByUnit[getString(unit, "unit")] = unit
	}
	for _, unit := range jp {
		id := getString(unit, "unit")
		cnUnit := cnByUnit[id]
		collectPair(out["unitName"], getString(unit, "unitName"), getString(cnUnit, "unitName"), true)
		collectPair(out["profileSentence"], getString(unit, "profileSentence"), getString(cnUnit, "profileSentence"), true)
	}
	return out, nil
}

func (t *Translator) extractSimpleNameByID(fileName, idField, nameField string) (map[string]map[string]string, error) {
	jp, err := t.fetchMasterdata(fileName, "jp")
	if err != nil {
		return nil, err
	}
	cn, err := t.fetchMasterdata(fileName, "cn")
	if err != nil {
		return nil, err
	}
	cnByID := byIntID(cn, idField)
	out := map[string]map[string]string{"name": {}}
	for _, item := range jp {
		id := getInt(item, idField)
		collectPair(out["name"], getString(item, nameField), getString(cnByID[id], nameField), true)
	}
	return out, nil
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

	dir := filepath.Join(t.store.path, "eventStory")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}

	processed := 0
	scenarioErrors := 0
	for _, jpStory := range jpStories {
		eventID := getInt(jpStory, "eventId")
		if !cnEventSet[eventID] {
			continue
		}
		cnStory := cnStoryByEvent[eventID]
		if cnStory == nil {
			continue
		}
		asset := getString(jpStory, "assetbundleName")
		jpEpisodes := toMapSlice(jpStory["eventStoryEpisodes"])
		cnEpisodes := toMapSlice(cnStory["eventStoryEpisodes"])
		cnByEp := byIntID(cnEpisodes, "episodeNo")

		type epData struct {
			ScenarioID string            `json:"scenarioId"`
			Title      string            `json:"title"`
			TalkData   map[string]string `json:"talkData"`
		}
		episodes := map[string]epData{}

		for _, ep := range jpEpisodes {
			epNo := getInt(ep, "episodeNo")
			scenarioID := getString(ep, "scenarioId")
			if scenarioID == "" {
				continue
			}
			scenarioPath := fmt.Sprintf("event_story/%s/scenario/%s", asset, scenarioID)
			jpScenario, err := t.fetchJSONURL(fmt.Sprintf("%s/%s.json", jpAssetsURL, scenarioPath))
			if err != nil {
				scenarioErrors++
				continue
			}
			cnScenario, err := t.fetchJSONURL(fmt.Sprintf("%s/%s.json", cnAssetsURL, scenarioPath))
			if err != nil {
				scenarioErrors++
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
			if len(talkData) == 0 {
				continue
			}
			episodes[strconv.Itoa(epNo)] = epData{
				ScenarioID: scenarioID,
				Title:      getString(cnByEp[epNo], "title"),
				TalkData:   talkData,
			}
		}

		if len(episodes) == 0 {
			continue
		}

		payload := map[string]any{
			"meta": map[string]any{
				"source":       "official_cn",
				"version":      "1.0",
				"last_updated": time.Now().Unix(),
			},
			"episodes": episodes,
		}
		b, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			return processed, err
		}
		outPath := filepath.Join(dir, fmt.Sprintf("event_%d.json", eventID))
		if err := writeAtomic(outPath, b); err != nil {
			return processed, err
		}
		processed++
	}

	if scenarioErrors > 0 {
		return processed, fmt.Errorf("event story scenario fetch failed: %d files", scenarioErrors)
	}

	return processed, nil
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
