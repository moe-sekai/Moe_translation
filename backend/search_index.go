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
	"strings"
	"time"
)

// SearchIndexBuilder regenerates search-index.json using masterdata and translations.
type SearchIndexBuilder struct {
	store           *Store
	dataDir         string
	client          *http.Client
	debounce        time.Duration
	refreshInterval time.Duration
	triggerCh       chan struct{}
	buildCh         chan struct{}
	stopCh          chan struct{}
	startedAt       time.Time
	lastUpdate      time.Time
}

type searchIndexEntry struct {
	ID int    `json:"id"`
	N  string `json:"n"`
	G  string `json:"g"`
	C  int    `json:"c,omitempty"`
	CN string `json:"cn,omitempty"`
}

// NewSearchIndexBuilder creates a debounce-backed generator with periodic refresh.
func NewSearchIndexBuilder(store *Store, dataDir string, debounce, refreshInterval time.Duration) *SearchIndexBuilder {
	if debounce <= 0 {
		debounce = time.Hour
	}
	if refreshInterval <= 0 {
		refreshInterval = time.Hour
	}
	if strings.TrimSpace(dataDir) == "" {
		dataDir = "./data"
	}
	return &SearchIndexBuilder{
		store:           store,
		dataDir:         dataDir,
		client:          &http.Client{Timeout: 40 * time.Second},
		debounce:        debounce,
		refreshInterval: refreshInterval,
		triggerCh:       make(chan struct{}, 1),
		buildCh:         make(chan struct{}, 1),
		stopCh:          make(chan struct{}),
	}
}

// Start begins the debounce loop.
func (b *SearchIndexBuilder) Start() {
	if b == nil {
		return
	}
	if b.store != nil {
		b.store.RegisterOnChange(b.Trigger)
	}
	b.startedAt = time.Now()
	go b.loop()
	// Generate once on startup so /data/search-index.json is available immediately.
	b.requestBuild()
}

// Trigger schedules a debounced rebuild.
func (b *SearchIndexBuilder) Trigger() {
	if b == nil {
		return
	}
	select {
	case b.triggerCh <- struct{}{}:
	default:
	}
}

func (b *SearchIndexBuilder) requestBuild() {
	if b == nil {
		return
	}
	select {
	case b.buildCh <- struct{}{}:
	default:
	}
}

// Stop terminates the debounce loop.
func (b *SearchIndexBuilder) Stop() {
	if b == nil {
		return
	}
	close(b.stopCh)
}

func (b *SearchIndexBuilder) loop() {
	var timer *time.Timer
	var refreshTicker *time.Ticker
	if b.refreshInterval > 0 {
		refreshTicker = time.NewTicker(b.refreshInterval)
		defer refreshTicker.Stop()
	}
	refreshCh := func() <-chan time.Time {
		if refreshTicker == nil {
			return nil
		}
		return refreshTicker.C
	}
	for {
		select {
		case <-b.stopCh:
			if timer != nil {
				timer.Stop()
			}
			return
		case <-b.buildCh:
			b.build("startup")
		case <-b.triggerCh:
			b.lastUpdate = time.Now()
			if timer == nil {
				timer = time.NewTimer(b.debounce)
			} else {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(b.debounce)
			}
		case <-func() <-chan time.Time {
			if timer == nil {
				return nil
			}
			return timer.C
		}():
			b.build("debounce")
			if timer != nil {
				timer.Stop()
				timer = nil
			}
		case <-refreshCh():
			b.build("refresh")
		}
	}
}

func (b *SearchIndexBuilder) build(reason string) {
	start := time.Now()
	index := make([]searchIndexEntry, 0, 4096)
	successes := 0

	// Load translations from store (full format).
	cardsTrans := b.snapshotCategory("cards")
	eventsTrans := b.snapshotCategory("events")
	gachaTrans := b.snapshotCategory("gacha")
	vlTrans := b.snapshotCategory("virtualLive")
	mysekaiTrans := b.snapshotCategory("mysekai")
	costumesTrans := b.snapshotCategory("costumes")

	getCN := func(trans TranslationCategory, field, jpText string) string {
		if trans == nil {
			return ""
		}
		fieldMap := trans[field]
		if fieldMap == nil {
			return ""
		}
		entry, ok := fieldMap[jpText]
		if !ok {
			return ""
		}
		return entry.Text
	}

	// Events (priority 1)
	events, err := b.fetchMasterdataArray("events.json", "jp")
	if err != nil {
		fmt.Printf("[search-index] events fetch failed: %v\n", err)
	} else {
		successes++
		for _, event := range events {
			name, _ := event["name"].(string)
			if strings.TrimSpace(name) == "" {
				continue
			}
			id := asInt(event["id"])
			entry := searchIndexEntry{ID: id, N: name, G: "events"}
			cn := getCN(eventsTrans, "name", name)
			if cn != "" && cn != name {
				entry.CN = cn
			}
			index = append(index, entry)
		}
	}

	// Music (priority 2)
	musics, err := b.fetchMasterdataArray("musics.json", "jp")
	musicTrans := b.snapshotCategory("music")
	if err != nil {
		fmt.Printf("[search-index] musics fetch failed: %v\n", err)
	} else {
		successes++
		for _, music := range musics {
			title, _ := music["title"].(string)
			if strings.TrimSpace(title) == "" {
				continue
			}
			id := asInt(music["id"])
			entry := searchIndexEntry{ID: id, N: title, G: "music"}
			cn := getCN(musicTrans, "title", title)
			if cn != "" && cn != title {
				entry.CN = cn
			}
			index = append(index, entry)
		}
	}

	// Cards (priority 3)
	cards, err := b.fetchMasterdataArray("cards.json", "jp")
	if err != nil {
		fmt.Printf("[search-index] cards fetch failed: %v\n", err)
	} else {
		successes++
		for _, card := range cards {
			prefix, _ := card["prefix"].(string)
			if strings.TrimSpace(prefix) == "" {
				continue
			}
			id := asInt(card["id"])
			charID := asInt(card["characterId"])
			entry := searchIndexEntry{ID: id, N: prefix, G: "cards", C: charID}
			cn := getCN(cardsTrans, "prefix", prefix)
			if cn != "" && cn != prefix {
				entry.CN = cn
			}
			index = append(index, entry)
		}
	}

	// Gacha (priority 4)
	gachas, err := b.fetchMasterdataArray("gachas.json", "jp")
	if err != nil {
		fmt.Printf("[search-index] gachas fetch failed: %v\n", err)
	} else {
		successes++
		for _, gacha := range gachas {
			name, _ := gacha["name"].(string)
			if strings.TrimSpace(name) == "" {
				continue
			}
			id := asInt(gacha["id"])
			entry := searchIndexEntry{ID: id, N: name, G: "gacha"}
			cn := getCN(gachaTrans, "name", name)
			if cn != "" && cn != name {
				entry.CN = cn
			}
			index = append(index, entry)
		}
	}

	// Mysekai (priority 5)
	fixtures, err := b.fetchMasterdataArray("mysekaiFixtures.json", "jp")
	if err != nil {
		fmt.Printf("[search-index] mysekai fixtures fetch failed: %v\n", err)
	} else {
		successes++
		for _, f := range fixtures {
			name, _ := f["name"].(string)
			if strings.TrimSpace(name) == "" {
				continue
			}
			id := asInt(f["id"])
			entry := searchIndexEntry{ID: id, N: name, G: "mysekai"}
			cn := getCN(mysekaiTrans, "fixtureName", name)
			if cn != "" && cn != name {
				entry.CN = cn
			}
			index = append(index, entry)
		}
	}

	// Costumes (priority 6)
	costumes, err := b.fetchCostumes()
	if err != nil {
		fmt.Printf("[search-index] costumes fetch failed: %v\n", err)
	} else {
		successes++
		for _, costume := range costumes {
			name, _ := costume["name"].(string)
			if strings.TrimSpace(name) == "" || name == "-" {
				continue
			}
			id := asInt(costume["id"])
			entry := searchIndexEntry{ID: id, N: name, G: "costumes"}
			cn := getCN(costumesTrans, "name", name)
			if cn != "" && cn != name {
				entry.CN = cn
			}
			index = append(index, entry)
		}
	}

	// Virtual Live (priority 7)
	lives, err := b.fetchMasterdataArray("virtualLives.json", "jp")
	if err != nil {
		fmt.Printf("[search-index] virtual lives fetch failed: %v\n", err)
	} else {
		successes++
		for _, live := range lives {
			name, _ := live["name"].(string)
			if strings.TrimSpace(name) == "" {
				continue
			}
			id := asInt(live["id"])
			entry := searchIndexEntry{ID: id, N: name, G: "live"}
			cn := getCN(vlTrans, "name", name)
			if cn != "" && cn != name {
				entry.CN = cn
			}
			index = append(index, entry)
		}
	}

	if successes == 0 {
		fmt.Printf("[search-index] all masterdata fetches failed; keeping existing index\n")
		return
	}

	outputPath := filepath.Join(b.dataDir, "search-index.json")
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		fmt.Printf("[search-index] mkdir failed: %v\n", err)
		return
	}
	buf, err := json.Marshal(index)
	if err != nil {
		fmt.Printf("[search-index] marshal failed: %v\n", err)
		return
	}
	if existing, err := os.ReadFile(outputPath); err == nil && bytes.Equal(existing, buf) {
		elapsed := time.Since(start).Round(time.Millisecond)
		fmt.Printf("[search-index] unchanged (%d entries) in %s\n", len(index), elapsed)
		return
	}
	if err := writeAtomic(outputPath, buf); err != nil {
		fmt.Printf("[search-index] write failed: %v\n", err)
		return
	}

	elapsed := time.Since(start).Round(time.Millisecond)
	if reason != "" {
		fmt.Printf("[search-index] generated %d entries in %s -> %s (reason=%s)\n", len(index), elapsed, outputPath, reason)
		return
	}
	fmt.Printf("[search-index] generated %d entries in %s -> %s\n", len(index), elapsed, outputPath)
}

func (b *SearchIndexBuilder) snapshotCategory(name string) TranslationCategory {
	if b.store == nil {
		return nil
	}
	b.store.mu.RLock()
	src := b.store.data[name]
	if src == nil {
		b.store.mu.RUnlock()
		return nil
	}
	dst := make(TranslationCategory, len(src))
	for field, entries := range src {
		copied := make(map[string]TranslationEntry, len(entries))
		for k, v := range entries {
			copied[k] = v
		}
		dst[field] = copied
	}
	b.store.mu.RUnlock()
	return dst
}

func (b *SearchIndexBuilder) fetchMasterdataArray(filename, server string) ([]map[string]any, error) {
	base := jpMasterdataURL
	if server == "cn" {
		base = cnMasterdataURL
	}
	url := fmt.Sprintf("%s/%s", base, filename)
	data, err := b.fetchJSONURL(url)
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

func (b *SearchIndexBuilder) fetchCostumes() ([]map[string]any, error) {
	base := jpMasterdataURL
	url := fmt.Sprintf("%s/%s", base, "snowy_costumes.json")
	data, err := b.fetchJSONURL(url)
	if err != nil {
		return nil, err
	}
	obj, ok := data.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("unexpected json type for snowy_costumes.json")
	}
	arr, ok := obj["costumes"].([]any)
	if !ok {
		return nil, fmt.Errorf("missing costumes in snowy_costumes.json")
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

func (b *SearchIndexBuilder) fetchJSONURL(url string) (any, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := b.client.Do(req)
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

func asInt(value any) int {
	switch v := value.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case json.Number:
		i, _ := v.Int64()
		return int(i)
	case string:
		if v == "" {
			return 0
		}
		var n int
		fmt.Sscanf(v, "%d", &n)
		return n
	default:
		return 0
	}
}
