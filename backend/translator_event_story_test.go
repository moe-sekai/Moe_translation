package backend

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseEventStoryDetailStructuredPreservesMeta(t *testing.T) {
	raw := []byte(`{
	  "meta": {
	    "source": "llm",
	    "version": "1.0",
	    "last_updated": 1772085158
	  },
	  "episodes": {
	    "1": {
	      "scenarioId": "event_165_01",
	      "title": "前一天",
	      "talkData": {
	        "まふゆ、大丈夫？": "真冬，你还好吗？"
	      }
	    }
	  }
	}`)

	detail, err := parseEventStoryDetail(raw, 123)
	if err != nil {
		t.Fatalf("parseEventStoryDetail returned error: %v", err)
	}
	if detail.Meta.Source != "llm" {
		t.Fatalf("expected source llm, got %q", detail.Meta.Source)
	}
	if detail.Meta.Version != "1.0" {
		t.Fatalf("expected version 1.0, got %q", detail.Meta.Version)
	}
	if detail.Meta.LastUpdated != 1772085158 {
		t.Fatalf("expected last_updated 1772085158, got %d", detail.Meta.LastUpdated)
	}
	if got := detail.Episodes["1"].TalkData["まふゆ、大丈夫？"]; got != "真冬，你还好吗？" {
		t.Fatalf("unexpected talkData value: %q", got)
	}
}

func TestParseEventStoryDetailLegacyNormalizesOfficialCN(t *testing.T) {
	raw := []byte(`{
	  "1": {
	    "scenarioId": "event_153_01",
	    "talkData": {
	      "それじゃあ——今年もお疲れさま！\nかんぱーい！": "那么——今年大家也辛苦了！\n干杯——！"
	    }
	  }
	}`)

	detail, err := parseEventStoryDetail(raw, 456)
	if err != nil {
		t.Fatalf("parseEventStoryDetail returned error: %v", err)
	}
	if detail.Meta.Source != "official_cn" {
		t.Fatalf("expected source official_cn, got %q", detail.Meta.Source)
	}
	if detail.Meta.Version != "legacy" {
		t.Fatalf("expected version legacy, got %q", detail.Meta.Version)
	}
	if detail.Meta.LastUpdated != 456 {
		t.Fatalf("expected fallback last_updated 456, got %d", detail.Meta.LastUpdated)
	}
	if got := detail.Episodes["1"].TalkData["それじゃあ——今年もお疲れさま！\nかんぱーい！"]; got != "那么——今年大家也辛苦了！\n干杯——！" {
		t.Fatalf("unexpected talkData value: %q", got)
	}
}

func TestLoadLocalEventStoryStatesTreatsLegacyEmptyCNAsOfficial(t *testing.T) {
	dir := t.TempDir()
	raw := []byte(`{
	  "1": {
	    "scenarioId": "event_153_01",
	    "talkData": {
	      "JP": ""
	    }
	  }
	}`)
	path := filepath.Join(dir, "event_153.json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	translator := &Translator{}
	states, maxID, err := translator.loadLocalEventStoryStates(dir)
	if err != nil {
		t.Fatalf("loadLocalEventStoryStates returned error: %v", err)
	}
	if maxID != 153 {
		t.Fatalf("expected maxID 153, got %d", maxID)
	}
	state, ok := states[153]
	if !ok {
		t.Fatal("expected state for event 153")
	}
	if state.Source != "official_cn_legacy" {
		t.Fatalf("expected source official_cn_legacy, got %q", state.Source)
	}
	if !state.IsOfficialCN {
		t.Fatal("expected IsOfficialCN to be true")
	}
	if state.IsLLM {
		t.Fatal("expected IsLLM to be false")
	}
}
