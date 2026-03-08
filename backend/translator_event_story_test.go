package backend

import (
	"encoding/json"
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

func TestGetEventStoryMergesCompanionLineSources(t *testing.T) {
	dir := t.TempDir()
	eventDir := filepath.Join(dir, "eventStory")
	if err := os.MkdirAll(eventDir, 0o755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	mainRaw := []byte(`{
	  "meta": {
	    "source": "official_cn",
	    "version": "1.0",
	    "last_updated": 1772085158
	  },
	  "episodes": {
	    "1": {
	      "scenarioId": "event_200_01",
	      "title": "标题",
	      "talkData": {
	        "JP1": "CN1",
	        "JP2": "CN2"
	      }
	    }
	  }
	}`)
	fullRaw := []byte(`{
	  "meta": {
	    "source": "official_cn",
	    "version": "1.0",
	    "last_updated": 1772085158
	  },
	  "episodes": {
	    "1": {
	      "scenarioId": "event_200_01",
	      "title": {
	        "text": "标题",
	        "source": "cn"
	      },
	      "talkData": {
	        "JP1": {
	          "text": "CN1",
	          "source": "human"
	        },
	        "JP2": {
	          "text": "CN2",
	          "source": "cn"
	        }
	      }
	    }
	  }
	}`)
	if err := os.WriteFile(filepath.Join(eventDir, "event_200.json"), mainRaw, 0o644); err != nil {
		t.Fatalf("WriteFile main failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(eventDir, "event_200.full.json"), fullRaw, 0o644); err != nil {
		t.Fatalf("WriteFile full failed: %v", err)
	}

	translator := &Translator{store: &Store{path: dir}}
	detail, err := translator.GetEventStory(200)
	if err != nil {
		t.Fatalf("GetEventStory returned error: %v", err)
	}
	if got := detail.Episodes["1"].TalkSources["JP1"]; got != SourceHuman {
		t.Fatalf("expected JP1 source human, got %q", got)
	}
	if got := detail.Episodes["1"].TalkSources["JP2"]; got != SourceCN {
		t.Fatalf("expected JP2 source cn, got %q", got)
	}
}

func TestGetEventStoryFallsBackWhenCompanionIsInvalid(t *testing.T) {
	dir := t.TempDir()
	eventDir := filepath.Join(dir, "eventStory")
	if err := os.MkdirAll(eventDir, 0o755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	mainRaw := []byte(`{
	  "meta": {
	    "source": "official_cn",
	    "version": "1.0",
	    "last_updated": 1772085158
	  },
	  "episodes": {
	    "1": {
	      "scenarioId": "event_202_01",
	      "title": "标题",
	      "talkData": {
	        "JP1": "CN1"
	      }
	    }
	  }
	}`)
	if err := os.WriteFile(filepath.Join(eventDir, "event_202.json"), mainRaw, 0o644); err != nil {
		t.Fatalf("WriteFile main failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(eventDir, "event_202.full.json"), []byte(`{"broken":true}`), 0o644); err != nil {
		t.Fatalf("WriteFile full failed: %v", err)
	}

	translator := &Translator{store: &Store{path: dir}}
	detail, err := translator.GetEventStory(202)
	if err != nil {
		t.Fatalf("GetEventStory returned error: %v", err)
	}
	if got := detail.Episodes["1"].TalkData["JP1"]; got != "CN1" {
		t.Fatalf("expected main talkData to remain readable, got %q", got)
	}
	if detail.Episodes["1"].TalkSources != nil {
		t.Fatalf("expected invalid sidecar to be ignored, got talkSources=%v", detail.Episodes["1"].TalkSources)
	}
}

func TestUpdateEventStoryLineCreatesCompanionAndPromotesHuman(t *testing.T) {
	dir := t.TempDir()
	eventDir := filepath.Join(dir, "eventStory")
	if err := os.MkdirAll(eventDir, 0o755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	raw := []byte(`{
	  "meta": {
	    "source": "official_cn",
	    "version": "1.0",
	    "last_updated": 1772085158
	  },
	  "episodes": {
	    "1": {
	      "scenarioId": "event_201_01",
	      "title": "标题",
	      "talkData": {
	        "JP": "CN"
	      }
	    }
	  }
	}`)
	if err := os.WriteFile(filepath.Join(eventDir, "event_201.json"), raw, 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	translator := &Translator{store: &Store{path: dir}}
	if err := translator.UpdateEventStoryLine(201, "1", "JP", "CN", "human"); err != nil {
		t.Fatalf("UpdateEventStoryLine returned error: %v", err)
	}

	mainData, err := os.ReadFile(filepath.Join(eventDir, "event_201.json"))
	if err != nil {
		t.Fatalf("ReadFile main failed: %v", err)
	}
	mainDetail, err := parseEventStoryDetail(mainData, 0)
	if err != nil {
		t.Fatalf("parseEventStoryDetail returned error: %v", err)
	}
	if mainDetail.Meta.Source != SourceHuman {
		t.Fatalf("expected main source human, got %q", mainDetail.Meta.Source)
	}

	fullData, err := os.ReadFile(filepath.Join(eventDir, "event_201.full.json"))
	if err != nil {
		t.Fatalf("ReadFile full failed: %v", err)
	}
	var fullDetail eventStoryFullDetail
	if err := json.Unmarshal(fullData, &fullDetail); err != nil {
		t.Fatalf("Unmarshal full detail failed: %v", err)
	}
	if fullDetail.Meta.Source != SourceHuman {
		t.Fatalf("expected full source human, got %q", fullDetail.Meta.Source)
	}
	if got := fullDetail.Episodes["1"].TalkData["JP"].Source; got != SourceHuman {
		t.Fatalf("expected line source human, got %q", got)
	}
}

func TestPromoteEventStoryHumanMarksWholeStoryHuman(t *testing.T) {
	dir := t.TempDir()
	eventDir := filepath.Join(dir, "eventStory")
	if err := os.MkdirAll(eventDir, 0o755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	mainRaw := []byte(`{
	  "meta": {
	    "source": "official_cn",
	    "version": "1.0",
	    "last_updated": 1772085158
	  },
	  "episodes": {
	    "1": {
	      "scenarioId": "event_204_01",
	      "title": "标题",
	      "talkData": {
	        "JP1": "CN1",
	        "JP2": "CN2"
	      }
	    }
	  }
	}`)
	if err := os.WriteFile(filepath.Join(eventDir, "event_204.json"), mainRaw, 0o644); err != nil {
		t.Fatalf("WriteFile main failed: %v", err)
	}

	translator := &Translator{store: &Store{path: dir}}
	if err := translator.PromoteEventStoryHuman(204); err != nil {
		t.Fatalf("PromoteEventStoryHuman returned error: %v", err)
	}

	mainData, err := os.ReadFile(filepath.Join(eventDir, "event_204.json"))
	if err != nil {
		t.Fatalf("ReadFile main failed: %v", err)
	}
	mainDetail, err := parseEventStoryDetail(mainData, 0)
	if err != nil {
		t.Fatalf("parseEventStoryDetail returned error: %v", err)
	}
	if mainDetail.Meta.Source != SourceHuman {
		t.Fatalf("expected main source human, got %q", mainDetail.Meta.Source)
	}

	fullData, err := os.ReadFile(filepath.Join(eventDir, "event_204.full.json"))
	if err != nil {
		t.Fatalf("ReadFile full failed: %v", err)
	}
	var fullDetail eventStoryFullDetail
	if err := json.Unmarshal(fullData, &fullDetail); err != nil {
		t.Fatalf("Unmarshal full detail failed: %v", err)
	}
	if fullDetail.Meta.Source != SourceHuman {
		t.Fatalf("expected full source human, got %q", fullDetail.Meta.Source)
	}
	if got := fullDetail.Episodes["1"].Title.Source; got != SourceHuman {
		t.Fatalf("expected title source human, got %q", got)
	}
	for key, line := range fullDetail.Episodes["1"].TalkData {
		if line.Source != SourceHuman {
			t.Fatalf("expected %s source human, got %q", key, line.Source)
		}
	}
}

func TestLoadLocalEventStoryStatesDoesNotPreserveBootstrapOnlyCompanion(t *testing.T) {
	dir := t.TempDir()
	mainRaw := []byte(`{
	  "meta": {
	    "source": "official_cn",
	    "version": "1.0",
	    "last_updated": 1772085158
	  },
	  "episodes": {
	    "1": {
	      "scenarioId": "event_203_01",
	      "title": "标题",
	      "talkData": {
	        "JP": "CN"
	      }
	    }
	  }
	}`)
	fullRaw := []byte(`{
	  "meta": {
	    "source": "official_cn",
	    "version": "1.0",
	    "last_updated": 1772085158
	  },
	  "episodes": {
	    "1": {
	      "scenarioId": "event_203_01",
	      "title": {
	        "text": "标题",
	        "source": "cn"
	      },
	      "talkData": {
	        "JP": {
	          "text": "CN",
	          "source": "cn"
	        }
	      }
	    }
	  }
	}`)
	if err := os.WriteFile(filepath.Join(dir, "event_203.json"), mainRaw, 0o644); err != nil {
		t.Fatalf("WriteFile main failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "event_203.full.json"), fullRaw, 0o644); err != nil {
		t.Fatalf("WriteFile full failed: %v", err)
	}

	translator := &Translator{}
	states, _, err := translator.loadLocalEventStoryStates(dir)
	if err != nil {
		t.Fatalf("loadLocalEventStoryStates returned error: %v", err)
	}
	state, ok := states[203]
	if !ok {
		t.Fatal("expected state for event 203")
	}
	if !state.HasCompanion {
		t.Fatal("expected HasCompanion to be true")
	}
	if state.PreserveLocal {
		t.Fatal("expected bootstrap-only companion not to preserve local state")
	}
}
