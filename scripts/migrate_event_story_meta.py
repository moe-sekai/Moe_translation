#!/usr/bin/env python3
import argparse
import json
import re
from pathlib import Path


EVENT_RE = re.compile(r"^event_(\d+)\.json$")


def normalize_story_source(source: str) -> str:
    value = (source or "").strip().lower()
    if value in {"official_cn", "llm", "jp_pending", "human", "pinned", "unknown"}:
        return value
    if value in {"official_cn_legacy", "cn"}:
        return "official_cn"
    return "unknown"


def normalize_line_source(source: str) -> str:
    value = (source or "").strip().lower()
    if value in {"cn", "human", "pinned", "llm", "unknown"}:
        return value
    if value in {"official_cn", "official_cn_legacy"}:
        return "cn"
    if value == "jp_pending":
        return "unknown"
    return "unknown"


def migrate_main_file(path: Path) -> tuple[bool, dict]:
    raw = json.loads(path.read_text(encoding="utf-8"))
    mtime = int(path.stat().st_mtime)
    if isinstance(raw, dict) and "meta" in raw and "episodes" in raw:
        raw["meta"] = {
            "source": normalize_story_source(
                raw.get("meta", {}).get("source", "unknown")
            ),
            "version": str(raw.get("meta", {}).get("version") or "1.0"),
            "last_updated": int(raw.get("meta", {}).get("last_updated") or mtime),
        }
        return False, raw

    episodes = {}
    for episode_no, episode in raw.items():
        if not str(episode_no).isdigit() or not isinstance(episode, dict):
            continue
        episodes[str(episode_no)] = {
            "scenarioId": episode.get("scenarioId", ""),
            "title": episode.get("title", "") or "",
            "talkData": episode.get("talkData", {}) or {},
        }
    if not episodes:
        raise ValueError(f"unsupported legacy format: {path}")
    return True, {
        "meta": {
            "source": "official_cn",
            "version": "1.0",
            "last_updated": mtime,
        },
        "episodes": episodes,
    }


def build_full_payload(main_payload: dict) -> dict:
    meta = main_payload.get("meta", {})
    line_source = normalize_line_source(meta.get("source", "unknown"))
    episodes = {}
    for episode_no, episode in (main_payload.get("episodes") or {}).items():
        talk_data = {}
        for jp, cn in (episode.get("talkData") or {}).items():
            talk_data[jp] = {
                "text": cn,
                "source": line_source,
            }
        episodes[str(episode_no)] = {
            "scenarioId": episode.get("scenarioId", ""),
            "title": {
                "text": episode.get("title", "") or "",
                "source": line_source,
            },
            "talkData": talk_data,
        }
    return {
        "meta": {
            "source": normalize_story_source(meta.get("source", "unknown")),
            "version": "1.0",
            "last_updated": int(meta.get("last_updated") or 0),
        },
        "episodes": episodes,
    }


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Migrate legacy eventStory JSON files to the structured meta format."
    )
    parser.add_argument(
        "path",
        nargs="?",
        default="translations/eventStory",
        help="eventStory directory path",
    )
    parser.add_argument(
        "--write", action="store_true", help="write migrated files back to disk"
    )
    parser.add_argument(
        "--bootstrap-full",
        action="store_true",
        help="create missing event_*.full.json sidecar files",
    )
    args = parser.parse_args()

    base = Path(args.path)
    if not base.exists() or not base.is_dir():
        raise SystemExit(f"invalid eventStory directory: {base}")

    legacy_count = 0
    normalized_count = 0
    full_created = 0

    for path in sorted(base.iterdir()):
        match = EVENT_RE.match(path.name)
        if not match:
            continue
        was_legacy, main_payload = migrate_main_file(path)
        if was_legacy:
            legacy_count += 1
        else:
            normalized_count += 1

        if args.write and was_legacy:
            path.write_text(
                json.dumps(main_payload, ensure_ascii=False, indent=2) + "\n",
                encoding="utf-8",
            )

        if args.bootstrap_full:
            full_path = path.with_name(path.stem + ".full.json")
            if not full_path.exists():
                full_created += 1
                if args.write:
                    full_payload = build_full_payload(main_payload)
                    full_path.write_text(
                        json.dumps(full_payload, ensure_ascii=False, indent=2) + "\n",
                        encoding="utf-8",
                    )

    mode = "WRITE" if args.write else "DRY-RUN"
    print(
        f"[{mode}] checked={legacy_count + normalized_count} legacy={legacy_count} structured={normalized_count} full_created={full_created}"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
