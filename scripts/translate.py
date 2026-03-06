#!/usr/bin/env python3
"""
Translation Script for Snowy Viewer
Extracts Japanese text from JP masterdata and translates to Chinese.

Translation Sources (in priority order):
1. CN Server masterdata (official translations, matched by ID)
2. LLM translation (Gemini or SiliconFlow Qwen as fallback)

Translation data includes source tracking for incremental updates:
- "cn": Official translation from CN server
- "llm": LLM-generated translation

Usage:
    python scripts/translate.py --help
    python scripts/translate.py --dry-run                  # Show what would be translated
    python scripts/translate.py --category cards           # Translate only cards
    python scripts/translate.py --cn-only                  # Only use CN translations, no LLM
    python scripts/translate.py --llm gemini               # Use Gemini instead of SiliconFlow
    python scripts/translate.py                            # Translate all categories
"""

import json
import os
import re
import sys
import argparse
import time
from pathlib import Path
from typing import Dict, List, Optional, Any, Tuple
import urllib.request
import urllib.error

# Configuration
BATCH_SIZE = 20  # Number of texts to translate in one API call
RATE_LIMIT_DELAY = 1.0  # Seconds between API calls

# LLM API Configurations
LLM_CONFIGS = {
    "qwen": {
        "url": "https://api.siliconflow.cn/v1/chat/completions",
        "model": "Qwen/Qwen3-8B",
        "env_key": "SILICONFLOW_API_KEY",
    },
    "gemini": {
        # URL is constructed dynamically using the model name in call_gemini_api()
        "url_base": "https://generativelanguage.googleapis.com/v1beta/models",
        "model": "gemini-flash-latest",
        "env_key": "GEMINI_API_KEY",
    }
}

# Paths
SCRIPT_DIR = Path(__file__).parent
PROJECT_ROOT = SCRIPT_DIR.parent
OUTPUT_DIR = PROJECT_ROOT / "translations"

# Server URLs
JP_MASTERDATA_URL = "https://sekaimaster.exmeaning.com/master"
CN_MASTERDATA_URL = "https://sekaimaster-cn.exmeaning.com/master"

# CN Assets URL for scenario data
CN_ASSETS_URL = "https://sekai-assets-bdf29c81.seiunx.net/cn-assets/ondemand"
JP_ASSETS_URL = "https://snowyassets.exmeaning.com/ondemand"

# Character name mapping (shared between prompts)
CHARACTER_NAME_CONTEXT = """角色译名表（日文 -> 中文）：
Virtual Singer(虚拟歌手):
- 初音ミク: 初音未来
- 鏡音リン: 镜音铃
- 鏡音レン: 镜音连
- 巡音ルカ: 巡音流歌
- MEIKO: MEIKO
- KAITO: KAITO

Leo/need (教室的世界):
- 星乃一歌: 星乃一歌
- 天馬咲希: 天马咲希
- 望月穂波: 望月穗波
- 日野森志歩: 日野森志步

MORE MORE JUMP! (舞台的世界):
- 花里みのり: 花里实乃里
- 桐谷遥: 桐谷遥
- 桃井愛莉: 桃井爱莉
- 日野森雫: 日野森雫

Vivid BAD SQUAD (街头的世界):
- 小豆沢こはね: 小豆泽心羽
- 白石杏: 白石杏
- 東雲彰人: 东云彰人
- 青柳冬弥: 青柳冬弥

Wonderlands×Showtime (奇幻的世界):
- 天馬司: 天马司
- 鳳えむ: 凤笑梦
- 草薙寧々: 草薙宁宁
- 神代類: 神代类

25時、ナイトコードで。(无人的世界):
- 宵崎奏: 宵崎奏
- 朝比奈まふゆ: 朝比奈真冬
- 東雲絵名: 东云绘名
- 暁山瑞希: 暁山瑞希
"""

# Game context prompt for LLM (general category translation)
GAME_CONTEXT_PROMPT = f"""你是一个专业的游戏翻译器，专门翻译《世界计划 彩色舞台 feat. 初音未来》(Project SEKAI) 游戏内容。

{CHARACTER_NAME_CONTEXT}

请将以下XML格式的日文文本翻译成简体中文。保持翻译简洁、自然，符合游戏风格。

请严格按照如下XML格式返回翻译结果，每个<t>标签的id必须与输入的<item>标签id一一对应：
<translations>
<t id="1">翻译结果1</t>
<t id="2">翻译结果2</t>
</translations>

注意：
- 只返回<translations>标签及其内容，不要添加任何额外说明
- 保持id与输入完全对应
- 不要遗漏任何条目

待翻译文本：
"""

# Story dialog translation prompt (preserves dialog formatting)
STORY_TRANSLATION_PROMPT = f"""你是一个专业的游戏翻译器，专门翻译《世界计划 彩色舞台 feat. 初音未来》(Project SEKAI) 游戏剧情对话。

游戏背景设定：
游戏中存在一个现实世界和虚拟世界「世界」，游戏的主人公团体（5个团体，每个团有4个角色）各有一个「世界」，他们能够通过电子设备上神秘出现的「Untitled」歌曲往返现实世界和「世界」。在「世界」中，原本在现实世界的虚拟歌手（比如初音未来）变为了真实的存在，能够与主人公团体互动。「世界」反映的是主人的强烈的心愿，而虚拟歌手们会帮助主人公团体一步步达成他们的心愿。

{CHARACTER_NAME_CONTEXT}

请将以下XML格式的日文游戏剧情对话翻译成简体中文。

翻译要求：
- 保持对话的语气、情感和角色个性
- 保留文本中的换行符（\n）
- 保留情感符号（♪、！、……、？等）
- 保留引号格式（『』用于表示消息/回忆中的对话）
- 角色名使用译名表中的中文名
- 翻译要自然流畅，符合中文口语习惯

请严格按照如下XML格式返回翻译结果，每个<t>标签的id必须与输入的<item>标签id一一对应：
<translations>
<t id="1">翻译结果1</t>
<t id="2">翻译结果2</t>
</translations>

注意：
- 只返回<translations>标签及其内容，不要添加任何额外说明
- 保持id与输入完全对应
- 不要遗漏任何条目

待翻译对话：
"""


def get_api_key(llm_type: str) -> str:
    """Get API key from environment variable"""
    config = LLM_CONFIGS[llm_type]
    api_key = os.environ.get(config["env_key"])
    if not api_key:
        print(f"Error: {config['env_key']} environment variable not set")
        print(f"Please set it with: set {config['env_key']}=your_api_key")
        sys.exit(1)
    return api_key


def fetch_masterdata(filename: str, server: str = "jp") -> Optional[Any]:
    """Fetch masterdata JSON from specified server"""
    base_url = JP_MASTERDATA_URL if server == "jp" else CN_MASTERDATA_URL
    url = f"{base_url}/{filename}"
    try:
        print(f"  Fetching [{server.upper()}] {filename}...")
        req = urllib.request.Request(url, headers={"Accept-Encoding": "gzip"})
        with urllib.request.urlopen(req, timeout=30) as response:
            data = response.read()
            if response.info().get("Content-Encoding") == "gzip":
                import gzip
                data = gzip.decompress(data)
            return json.loads(data.decode("utf-8"))
    except urllib.error.URLError as e:
        print(f"  Warning: Failed to fetch {filename} from {server}: {e}")
        return None
    except json.JSONDecodeError as e:
        print(f"  Warning: Failed to parse {filename}: {e}")
        return None


# ============================================================================
# XML-based LLM input/output helpers
# ============================================================================

def build_xml_input(texts: List[str]) -> str:
    """Build XML-formatted input for LLM translation.
    
    Each text is wrapped in an <item> tag with an id attribute.
    Special XML characters in text are escaped.
    """
    lines = []
    for i, text in enumerate(texts, 1):
        # Escape XML special characters
        escaped = text.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")
        lines.append(f'<item id="{i}">{escaped}</item>')
    return "\n".join(lines)


def parse_xml_translations(content: str, expected_count: int) -> Dict[int, str]:
    """Parse XML-formatted translation response from LLM.
    
    Extracts <t id="N">translated text</t> from the response.
    Returns a dict mapping id (1-based) to translated text.
    """
    # Remove think blocks (Qwen sometimes adds these)
    content = re.sub(r'<think>.*?</think>', '', content, flags=re.DOTALL)
    
    # Extract translations using regex - allow multiline content inside <t> tags
    pattern = r'<t\s+id="(\d+)">(.*?)</t>'
    matches = re.findall(pattern, content, re.DOTALL)
    
    result = {}
    for id_str, text in matches:
        tid = int(id_str)
        # Unescape XML entities
        text = text.replace("&lt;", "<").replace("&gt;", ">").replace("&amp;", "&")
        result[tid] = text.strip()
    
    if len(result) != expected_count:
        print(f"  Warning: Expected {expected_count} translations, got {len(result)}")
    
    return result


def call_qwen_api(api_key: str, texts: List[str], prompt_template: str = "", retries: int = 3) -> List[str]:
    """Call SiliconFlow Qwen API with XML format and retry logic"""
    config = LLM_CONFIGS["qwen"]
    if not prompt_template:
        prompt_template = GAME_CONTEXT_PROMPT
    prompt = prompt_template + build_xml_input(texts)
    
    payload = {
        "model": config["model"],
        "messages": [{"role": "user", "content": prompt}],
        "temperature": 0.3,
        "max_tokens": 8192,
    }
    
    headers = {
        "Authorization": f"Bearer {api_key}",
        "Content-Type": "application/json",
    }
    
    for attempt in range(retries):
        try:
            data = json.dumps(payload).encode("utf-8")
            req = urllib.request.Request(config["url"], data=data, headers=headers, method="POST")
            
            with urllib.request.urlopen(req, timeout=180) as response:
                result = json.loads(response.read().decode("utf-8"))
                content = result["choices"][0]["message"]["content"]
                
                parsed = parse_xml_translations(content, len(texts))
                if len(parsed) >= len(texts) * 0.5:  # At least 50% success
                    return [parsed.get(i, "") for i in range(1, len(texts) + 1)]
                else:
                    print(f"  Qwen: Low parse rate ({len(parsed)}/{len(texts)}), retrying...")
                
        except urllib.error.HTTPError as e:
            error_body = ""
            try:
                error_body = e.read().decode("utf-8")[:500]
            except Exception:
                pass
            print(f"  Qwen API HTTP {e.code} (attempt {attempt+1}/{retries}): {e.reason}")
            if error_body:
                print(f"    Response: {error_body}")
            # Don't retry on client errors (4xx) except 429 (rate limit)
            if 400 <= e.code < 500 and e.code != 429:
                print(f"  Skipping retries for HTTP {e.code} (client error)")
                return []
        except Exception as e:
            print(f"  Qwen API Error (attempt {attempt+1}/{retries}): {e}")
        
        if attempt < retries - 1:
            wait = 2 ** (attempt + 1)
            print(f"  Retrying in {wait}s...")
            time.sleep(wait)
    
    return []


def call_gemini_api(api_key: str, texts: List[str], prompt_template: str = "", retries: int = 3) -> List[str]:
    """Call Google Gemini API with XML format and retry logic"""
    config = LLM_CONFIGS["gemini"]
    if not prompt_template:
        prompt_template = GAME_CONTEXT_PROMPT
    prompt = prompt_template + build_xml_input(texts)
    
    # Build URL dynamically from model name
    url = f"{config['url_base']}/{config['model']}:generateContent?key={api_key}"
    
    payload = {
        "contents": [{"parts": [{"text": prompt}]}],
        "generationConfig": {
            "temperature": 0.3,
            "maxOutputTokens": 16384,
        }
    }
    
    headers = {"Content-Type": "application/json"}
    
    for attempt in range(retries):
        try:
            data = json.dumps(payload).encode("utf-8")
            req = urllib.request.Request(url, data=data, headers=headers, method="POST")
            
            with urllib.request.urlopen(req, timeout=180) as response:
                result = json.loads(response.read().decode("utf-8"))
                content = result["candidates"][0]["content"]["parts"][0]["text"]
                
                parsed = parse_xml_translations(content, len(texts))
                if len(parsed) >= len(texts) * 0.5:  # At least 50% success
                    return [parsed.get(i, "") for i in range(1, len(texts) + 1)]
                else:
                    print(f"  Gemini: Low parse rate ({len(parsed)}/{len(texts)}), retrying...")
                
        except urllib.error.HTTPError as e:
            error_body = ""
            try:
                error_body = e.read().decode("utf-8")[:500]
            except Exception:
                pass
            print(f"  Gemini API HTTP {e.code} (attempt {attempt+1}/{retries}): {e.reason}")
            if error_body:
                print(f"    Response: {error_body}")
            # Don't retry on client errors (4xx) except 429 (rate limit)
            if 400 <= e.code < 500 and e.code != 429:
                print(f"  Skipping retries for HTTP {e.code} (client error)")
                return []
        except Exception as e:
            print(f"  Gemini API Error (attempt {attempt+1}/{retries}): {e}")
        
        if attempt < retries - 1:
            wait = 2 ** (attempt + 1)
            print(f"  Retrying in {wait}s...")
            time.sleep(wait)
    
    return []


def call_llm(api_key: str, texts: List[str], llm_type: str, prompt_template: str = "") -> List[str]:
    """Call LLM API to translate texts using XML format.
    
    If the initial call fails or returns too few results, automatically
    splits the batch in half and retries.
    """
    if not texts:
        return []
    
    if llm_type == "gemini":
        result = call_gemini_api(api_key, texts, prompt_template)
    else:
        result = call_qwen_api(api_key, texts, prompt_template)
    
    # Check if we got enough results
    non_empty = sum(1 for r in result if r)
    if non_empty < len(texts) * 0.5 and len(texts) > 3:
        # Auto-split: try smaller batches
        print(f"  Auto-splitting batch ({non_empty}/{len(texts)} succeeded), retrying in 2 halves...")
        time.sleep(RATE_LIMIT_DELAY)
        mid = len(texts) // 2
        first_half = call_llm(api_key, texts[:mid], llm_type, prompt_template)
        time.sleep(RATE_LIMIT_DELAY)
        second_half = call_llm(api_key, texts[mid:], llm_type, prompt_template)
        return first_half + second_half
    
    return result


def translate_batch(api_key: str, texts: List[str], llm_type: str, dry_run: bool = False, prompt_template: str = "") -> Dict[str, str]:
    """Translate a batch of texts using LLM and return mapping"""
    if dry_run:
        return {t: f"[LLM翻译] {t}" for t in texts}
    
    translations = call_llm(api_key, texts, llm_type, prompt_template)
    
    result = {}
    missing = 0
    for i, original in enumerate(texts):
        if i < len(translations):
            translated = translations[i]
            if translated:
                result[original] = translated
            else:
                missing += 1
        else:
            missing += 1
    
    if missing > 0:
        print(f"  Warning: {missing}/{len(texts)} translations missing in this batch")
    
    return result


# ============================================================================
# Translation data structure with source tracking
# Format: { "field": { "jp_text": { "text": "cn_text", "source": "cn" | "llm" } } }
# For backward compatibility, also supports flat format: { "field": { "jp_text": "cn_text" } }
# ============================================================================

def normalize_translation_data(data: Dict[str, Any]) -> Dict[str, Dict[str, Dict[str, str]]]:
    """Convert flat format to structured format with source tracking"""
    result = {}
    for field, translations in data.items():
        if not isinstance(translations, dict):
            continue
        result[field] = {}
        for jp_text, value in translations.items():
            if isinstance(value, dict) and "text" in value:
                # Already in new format
                result[field][jp_text] = value
            else:
                # Old flat format, assume it's from LLM (or unknown)
                result[field][jp_text] = {"text": str(value), "source": "unknown"}
    return result


def flatten_translation_data(data: Dict[str, Dict[str, Dict[str, str]]]) -> Dict[str, Dict[str, str]]:
    """Convert structured format back to flat format for frontend consumption"""
    result = {}
    for field, translations in data.items():
        result[field] = {}
        for jp_text, value in translations.items():
            if isinstance(value, dict) and "text" in value:
                result[field][jp_text] = value["text"]
            else:
                result[field][jp_text] = str(value)
    return result


# ============================================================================
# Extraction functions that get both JP and CN data
# ============================================================================

def extract_cards_with_cn() -> Tuple[Dict[str, Dict[str, str]], Dict[str, List[str]]]:
    """Extract card translations from CN server, and JP texts without CN translation"""
    jp_data = fetch_masterdata("cards.json", "jp")
    cn_data = fetch_masterdata("cards.json", "cn")
    
    cn_translations = {"prefix": {}, "skillName": {}, "gachaPhrase": {}}
    jp_only = {"prefix": [], "skillName": [], "gachaPhrase": []}
    
    if not jp_data:
        return cn_translations, jp_only
    
    cn_by_id = {c["id"]: c for c in (cn_data or [])}
    
    for card in jp_data:
        card_id = card["id"]
        cn_card = cn_by_id.get(card_id)
        
        jp_prefix = card.get("prefix", "")
        if jp_prefix:
            if cn_card and cn_card.get("prefix") and cn_card["prefix"] != jp_prefix:
                cn_translations["prefix"][jp_prefix] = cn_card["prefix"]
            else:
                jp_only["prefix"].append(jp_prefix)
        
        jp_skill = card.get("cardSkillName", "")
        if jp_skill:
            if cn_card and cn_card.get("cardSkillName") and cn_card["cardSkillName"] != jp_skill:
                cn_translations["skillName"][jp_skill] = cn_card["cardSkillName"]
            else:
                jp_only["skillName"].append(jp_skill)

        jp_phrase = card.get("gachaPhrase", "")
        if jp_phrase and jp_phrase != "-":
            if cn_card and cn_card.get("gachaPhrase") and cn_card["gachaPhrase"] != jp_phrase:
                cn_translations["gachaPhrase"][jp_phrase] = cn_card["gachaPhrase"]
            else:
                jp_only["gachaPhrase"].append(jp_phrase)
    
    return cn_translations, jp_only


def extract_events_with_cn() -> Tuple[Dict[str, Dict[str, str]], Dict[str, List[str]]]:
    """Extract event translations from CN server"""
    jp_data = fetch_masterdata("events.json", "jp")
    cn_data = fetch_masterdata("events.json", "cn")
    
    cn_translations = {"name": {}}
    jp_only = {"name": []}
    
    if not jp_data:
        return cn_translations, jp_only
    
    cn_by_id = {e["id"]: e for e in (cn_data or [])}
    
    for event in jp_data:
        event_id = event["id"]
        cn_event = cn_by_id.get(event_id)
        
        jp_name = event.get("name", "")
        if jp_name:
            if cn_event and cn_event.get("name") and cn_event["name"] != jp_name:
                cn_translations["name"][jp_name] = cn_event["name"]
            else:
                jp_only["name"].append(jp_name)
    
    return cn_translations, jp_only


def extract_gacha_with_cn() -> Tuple[Dict[str, Dict[str, str]], Dict[str, List[str]]]:
    """Extract gacha translations from CN server"""
    jp_data = fetch_masterdata("gachas.json", "jp")
    cn_data = fetch_masterdata("gachas.json", "cn")
    
    cn_translations = {"name": {}}
    jp_only = {"name": []}
    
    if not jp_data:
        return cn_translations, jp_only
    
    cn_by_id = {g["id"]: g for g in (cn_data or [])}
    
    for gacha in jp_data:
        gacha_id = gacha["id"]
        cn_gacha = cn_by_id.get(gacha_id)
        
        jp_name = gacha.get("name", "")
        if jp_name:
            if cn_gacha and cn_gacha.get("name") and cn_gacha["name"] != jp_name:
                cn_translations["name"][jp_name] = cn_gacha["name"]
            else:
                jp_only["name"].append(jp_name)
    
    return cn_translations, jp_only


def extract_virtual_live_with_cn() -> Tuple[Dict[str, Dict[str, str]], Dict[str, List[str]]]:
    """Extract virtual live translations from CN server"""
    jp_data = fetch_masterdata("virtualLives.json", "jp")
    cn_data = fetch_masterdata("virtualLives.json", "cn")
    
    cn_translations = {"name": {}}
    jp_only = {"name": []}
    
    if not jp_data:
        return cn_translations, jp_only
    
    cn_by_id = {v["id"]: v for v in (cn_data or [])}
    
    for vl in jp_data:
        vl_id = vl["id"]
        cn_vl = cn_by_id.get(vl_id)
        
        jp_name = vl.get("name", "")
        if jp_name:
            if cn_vl and cn_vl.get("name") and cn_vl["name"] != jp_name:
                cn_translations["name"][jp_name] = cn_vl["name"]
            else:
                jp_only["name"].append(jp_name)
    
    return cn_translations, jp_only


def extract_sticker_with_cn() -> Tuple[Dict[str, Dict[str, str]], Dict[str, List[str]]]:
    """Extract sticker translations from CN server"""
    jp_data = fetch_masterdata("stamps.json", "jp")
    cn_data = fetch_masterdata("stamps.json", "cn")
    
    cn_translations = {"name": {}}
    jp_only = {"name": []}
    
    if not jp_data:
        return cn_translations, jp_only
    
    cn_by_id = {s["id"]: s for s in (cn_data or [])}
    
    for stamp in jp_data:
        stamp_id = stamp["id"]
        cn_stamp = cn_by_id.get(stamp_id)
        
        jp_name = stamp.get("name", "")
        if jp_name:
            if cn_stamp and cn_stamp.get("name") and cn_stamp["name"] != jp_name:
                cn_translations["name"][jp_name] = cn_stamp["name"]
            else:
                jp_only["name"].append(jp_name)
    
    return cn_translations, jp_only


def extract_comic_with_cn() -> Tuple[Dict[str, Dict[str, str]], Dict[str, List[str]]]:
    """Extract comic translations from CN server (using tips.json)"""
    # Note: Snowy Viewer uses tips.json with assetbundleName for comics
    jp_data = fetch_masterdata("tips.json", "jp")
    cn_data = fetch_masterdata("tips.json", "cn")
    
    cn_translations = {"title": {}}
    jp_only = {"title": []}
    
    if not jp_data:
        return cn_translations, jp_only
    
    # Filter for comics (tips that have assetbundleName)
    jp_comics = [c for c in jp_data if c.get("assetbundleName")]
    
    cn_by_id = {c["id"]: c for c in (cn_data or [])}
    
    for comic in jp_comics:
        comic_id = comic["id"]
        cn_comic = cn_by_id.get(comic_id)
        
        jp_title = comic.get("title", "")
        if jp_title:
            if cn_comic and cn_comic.get("title") and cn_comic["title"] != jp_title:
                cn_translations["title"][jp_title] = cn_comic["title"]
            else:
                jp_only["title"].append(jp_title)
    
    return cn_translations, jp_only


def extract_music_texts() -> Tuple[Dict[str, Dict[str, str]], Dict[str, List[str]]]:
    """Extract music texts - CN doesn't have music translations, LLM only"""
    musics = fetch_masterdata("musics.json", "jp")
    vocals = fetch_masterdata("musicVocals.json", "jp")
    
    cn_translations = {"title": {}, "artist": {}, "vocalCaption": {}}
    jp_only = {"title": [], "artist": [], "vocalCaption": []}
    
    if musics:
        for music in musics:
            if music.get("title"):
                jp_only["title"].append(music["title"])
            for field in ["lyricist", "composer", "arranger"]:
                if music.get(field) and music[field] != "-":
                    jp_only["artist"].append(music[field])
    
    if vocals:
        for vocal in vocals:
            if vocal.get("caption"):
                jp_only["vocalCaption"].append(vocal["caption"])
    
    for key in jp_only:
        jp_only[key] = list(set(jp_only[key]))
    
    return cn_translations, jp_only


def extract_mysekai_with_cn() -> Tuple[Dict[str, Dict[str, str]], Dict[str, List[str]]]:
    """Extract mysekai translations from CN server"""
    jp_fixtures = fetch_masterdata("mysekaiFixtures.json", "jp")
    cn_fixtures = fetch_masterdata("mysekaiFixtures.json", "cn")
    jp_genres = fetch_masterdata("mysekaiFixtureMainGenres.json", "jp")
    cn_genres = fetch_masterdata("mysekaiFixtureMainGenres.json", "cn")
    jp_tags = fetch_masterdata("mysekaiFixtureTags.json", "jp")
    cn_tags = fetch_masterdata("mysekaiFixtureTags.json", "cn")
    
    cn_translations = {"fixtureName": {}, "flavorText": {}, "genre": {}, "tag": {}}
    jp_only = {"fixtureName": [], "flavorText": [], "genre": [], "tag": []}
    
    if jp_fixtures:
        cn_fix_by_id = {f["id"]: f for f in (cn_fixtures or [])}
        for f in jp_fixtures:
            fid = f["id"]
            cn_f = cn_fix_by_id.get(fid)
            
            jp_name = f.get("name", "")
            if jp_name:
                if cn_f and cn_f.get("name") and cn_f["name"] != jp_name:
                    cn_translations["fixtureName"][jp_name] = cn_f["name"]
                else:
                    jp_only["fixtureName"].append(jp_name)
            
            jp_flavor = f.get("flavorText", "")
            if jp_flavor:
                if cn_f and cn_f.get("flavorText") and cn_f["flavorText"] != jp_flavor:
                    cn_translations["flavorText"][jp_flavor] = cn_f["flavorText"]
                else:
                    jp_only["flavorText"].append(jp_flavor)
    
    if jp_genres:
        cn_gen_by_id = {g["id"]: g for g in (cn_genres or [])}
        for g in jp_genres:
            gid = g["id"]
            cn_g = cn_gen_by_id.get(gid)
            jp_name = g.get("name", "")
            if jp_name:
                if cn_g and cn_g.get("name") and cn_g["name"] != jp_name:
                    cn_translations["genre"][jp_name] = cn_g["name"]
                else:
                    jp_only["genre"].append(jp_name)
    
    if jp_tags:
        cn_tag_by_id = {t["id"]: t for t in (cn_tags or [])}
        for t in jp_tags:
            tid = t["id"]
            cn_t = cn_tag_by_id.get(tid)
            jp_name = t.get("name", "")
            if jp_name:
                if cn_t and cn_t.get("name") and cn_t["name"] != jp_name:
                    cn_translations["tag"][jp_name] = cn_t["name"]
                else:
                    jp_only["tag"].append(jp_name)
    
    for key in jp_only:
        jp_only[key] = list(set(jp_only[key]))
    
    return cn_translations, jp_only


def extract_costumes_with_cn() -> Tuple[Dict[str, Dict[str, str]], Dict[str, List[str]]]:
    """Extract costume translations from CN server (snowy_costumes.json)"""
    jp_data = fetch_masterdata("snowy_costumes.json", "jp")
    cn_data = fetch_masterdata("snowy_costumes.json", "cn")

    cn_translations = {"name": {}, "colorName": {}, "designer": {}}
    jp_only = {"name": [], "colorName": [], "designer": []}

    if not jp_data:
        return cn_translations, jp_only

    jp_costumes = jp_data.get("costumes", [])
    cn_costumes = (cn_data or {}).get("costumes", [])
    cn_by_id = {c["id"]: c for c in cn_costumes}

    for costume in jp_costumes:
        costume_id = costume["id"]
        cn_costume = cn_by_id.get(costume_id)

        # Name
        jp_name = costume.get("name", "")
        if jp_name and jp_name != "-":
            if cn_costume and cn_costume.get("name") and cn_costume["name"] != jp_name:
                cn_translations["name"][jp_name] = cn_costume["name"]
            else:
                jp_only["name"].append(jp_name)

        # Designer
        jp_designer = costume.get("designer", "")
        if jp_designer and jp_designer != "-":
            if cn_costume and cn_costume.get("designer") and cn_costume["designer"] != jp_designer:
                cn_translations["designer"][jp_designer] = cn_costume["designer"]
            else:
                jp_only["designer"].append(jp_designer)

        # ColorName - iterate all parts
        cn_parts = cn_costume.get("parts", {}) if cn_costume else {}
        for part_type, part_list in costume.get("parts", {}).items():
            cn_part_list = cn_parts.get(part_type, [])
            cn_part_by_asset = {p["assetbundleName"]: p for p in cn_part_list}

            for part in part_list:
                jp_color = part.get("colorName", "")
                if not jp_color or jp_color == "-":
                    continue
                cn_part = cn_part_by_asset.get(part["assetbundleName"])
                if cn_part and cn_part.get("colorName") and cn_part["colorName"] != jp_color:
                    cn_translations["colorName"][jp_color] = cn_part["colorName"]
                else:
                    jp_only["colorName"].append(jp_color)

    for key in jp_only:
        jp_only[key] = list(set(jp_only[key]))

    return cn_translations, jp_only


def extract_characters_with_cn() -> Tuple[Dict[str, Dict[str, str]], Dict[str, List[str]]]:
    """
    Extract character profile translations from CN server (characterProfiles.json).
    Fields: hobby, specialSkill, favoriteFood, hatedFood, weak, introduction
    """
    jp_profiles = fetch_masterdata("characterProfiles.json", "jp")
    cn_profiles = fetch_masterdata("characterProfiles.json", "cn")
    
    fields = ["hobby", "specialSkill", "favoriteFood", "hatedFood", "weak", "introduction"]
    cn_translations = {f: {} for f in fields}
    jp_only = {f: [] for f in fields}
    
    if not jp_profiles:
        return cn_translations, jp_only
        
    cn_by_id = {p["characterId"]: p for p in (cn_profiles or [])}
    
    for profile in jp_profiles:
        char_id = profile["characterId"]
        cn_profile = cn_by_id.get(char_id)
        
        for field in fields:
            jp_text = profile.get(field, "")
            # Skip empty or placeholder texts
            if not jp_text or jp_text == "-":
                continue
                
            if cn_profile and cn_profile.get(field) and cn_profile[field] != jp_text:
                cn_translations[field][jp_text] = cn_profile[field]
            else:
                jp_only[field].append(jp_text)
                
    for key in jp_only:
        jp_only[key] = list(set(jp_only[key]))
        
    return cn_translations, jp_only


def extract_units_with_cn() -> Tuple[Dict[str, Dict[str, str]], Dict[str, List[str]]]:
    """
    Extract unit profile translations from CN server (unitProfiles.json).
    Fields: unitName, profileSentence
    """
    jp_units = fetch_masterdata("unitProfiles.json", "jp")
    cn_units = fetch_masterdata("unitProfiles.json", "cn")
    
    fields = ["unitName", "profileSentence"]
    cn_translations = {f: {} for f in fields}
    jp_only = {f: [] for f in fields}
    
    if not jp_units:
        return cn_translations, jp_only
        
    cn_by_unit = {u["unit"]: u for u in (cn_units or [])}
    
    for unit in jp_units:
        unit_id = unit["unit"]
        cn_unit = cn_by_unit.get(unit_id)
        
        for field in fields:
            jp_text = unit.get(field, "")
            if not jp_text:
                continue
                
            if cn_unit and cn_unit.get(field) and cn_unit[field] != jp_text:
                cn_translations[field][jp_text] = cn_unit[field]
            else:
                jp_only[field].append(jp_text)
                
    for key in jp_only:
        jp_only[key] = list(set(jp_only[key]))
        
    return cn_translations, jp_only


# ============================================================================
# Main translation logic with source tracking
# ============================================================================

def load_existing_translations(category: str) -> Dict[str, Dict[str, Dict[str, str]]]:
    """Load existing translations from file (with source tracking)"""
    # Try loading full data first (preserves source info)
    filepath_full = OUTPUT_DIR / f"{category}.full.json"
    if filepath_full.exists():
        try:
            with open(filepath_full, "r", encoding="utf-8") as f:
                data = json.load(f)
                return normalize_translation_data(data)
        except (json.JSONDecodeError, IOError):
            pass
            
    # Fallback to flat file
    filepath = OUTPUT_DIR / f"{category}.json"
    if filepath.exists():
        try:
            with open(filepath, "r", encoding="utf-8") as f:
                data = json.load(f)
                return normalize_translation_data(data)
        except (json.JSONDecodeError, IOError):
            pass
    return {}


def save_translations(category: str, data: Dict[str, Dict[str, Dict[str, str]]], include_source: bool = True):
    """Save translations to file"""
    OUTPUT_DIR.mkdir(parents=True, exist_ok=True)
    
    # Save with source tracking (for developers)
    filepath_full = OUTPUT_DIR / f"{category}.full.json"
    with open(filepath_full, "w", encoding="utf-8") as f:
        json.dump(data, f, ensure_ascii=False, indent=2)
    print(f"  Saved full data (with source) to {filepath_full}")
    
    # Save flattened version (for frontend)
    filepath = OUTPUT_DIR / f"{category}.json"
    flat_data = flatten_translation_data(data)
    with open(filepath, "w", encoding="utf-8") as f:
        json.dump(flat_data, f, ensure_ascii=False, indent=2)
    print(f"  Saved flat data (for frontend) to {filepath}")


def translate_category_enhanced(
    api_key: str, 
    category: str, 
    extract_func, 
    llm_type: str = "qwen",
    dry_run: bool = False,
    cn_only: bool = False
):
    """Translate a category using CN server first, then LLM fallback"""
    print(f"\n{'='*60}")
    print(f"Translating: {category}")
    print(f"{'='*60}")
    
    # Extract translations from CN and texts needing LLM
    print("Extracting texts from JP & CN servers...")
    cn_translations, jp_only_texts = extract_func()
    
    # Load existing translations (with source tracking)
    existing = load_existing_translations(category)
    
    # Stats
    stats = {"cn_new": 0, "cn_updated": 0, "llm_new": 0}
    
    # Process each field
    for field in set(list(cn_translations.keys()) + list(jp_only_texts.keys())):
        existing_field = existing.get(field, {})
        cn_field = cn_translations.get(field, {})
        jp_field = jp_only_texts.get(field, [])
        
        # Process CN translations
        for jp_text, cn_text in cn_field.items():
            if jp_text not in existing_field:
                existing_field[jp_text] = {"text": cn_text, "source": "cn"}
                stats["cn_new"] += 1
            elif existing_field[jp_text].get("source") == "pinned":
                pass  # Never overwrite pinned entries
            elif existing_field[jp_text].get("source") != "cn" or existing_field[jp_text].get("text") != cn_text:
                # CN overwrites everything except pinned (including human, llm, unknown)
                existing_field[jp_text] = {"text": cn_text, "source": "cn"}
                stats["cn_updated"] += 1
        
        print(f"  {field}: CN new={stats['cn_new']}, CN updated={stats['cn_updated']}")
        
        # Filter JP texts that still need LLM translation
        # LLM only translates entries that don't exist or are 'unknown'
        need_llm = [t for t in jp_field if t and (
            t not in existing_field or existing_field[t].get("source") == "unknown"
        )]
        
        if not need_llm:
            pass  # All covered
        elif cn_only:
            print(f"  {field}: {len(need_llm)} texts need LLM (skipped, --cn-only mode)")
        else:
            print(f"  {field}: {len(need_llm)} texts need LLM translation")
            
            for i in range(0, len(need_llm), BATCH_SIZE):
                batch = need_llm[i:i+BATCH_SIZE]
                print(f"    LLM Batch {i//BATCH_SIZE + 1}/{(len(need_llm)-1)//BATCH_SIZE + 1} ({len(batch)} texts)")
                
                translations = translate_batch(api_key, batch, llm_type, dry_run)
                for jp_text, cn_text in translations.items():
                    existing_field[jp_text] = {"text": cn_text, "source": "llm"}
                    stats["llm_new"] += 1
                
                if not dry_run and i + BATCH_SIZE < len(need_llm):
                    time.sleep(RATE_LIMIT_DELAY)
        
        existing[field] = existing_field
    
    print(f"  Summary: CN new={stats['cn_new']}, CN updated={stats['cn_updated']}, LLM={stats['llm_new']}")
    
    # Save results
    if not dry_run:
        save_translations(category, existing)
    else:
        print(f"  [DRY RUN] Would save to {OUTPUT_DIR / f'{category}.json'}")


# ============================================================================
# Event Story Translation (per-event files)
# ============================================================================

def fetch_scenario_json(scenario_path: str) -> Optional[Any]:
    """Fetch scenario JSON from CN assets server"""
    url = f"{CN_ASSETS_URL}/{scenario_path}.json"
    return fetch_scenario_json_from_url(url)


def fetch_scenario_json_from_url(url: str, retries: int = 3) -> Optional[Any]:
    """Fetch scenario JSON from any URL with retries"""
    headers = {
        "User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
        "Accept-Encoding": "gzip"
    }
    
    import http.client
    
    for attempt in range(retries):
        try:
            req = urllib.request.Request(url, headers=headers)
            with urllib.request.urlopen(req, timeout=30) as response:
                data = response.read()
                if response.info().get("Content-Encoding") == "gzip":
                    import gzip
                    data = gzip.decompress(data)
                return json.loads(data.decode("utf-8"))
        except (urllib.error.URLError, http.client.IncompleteRead, ConnectionResetError) as e:
            if attempt == retries - 1:
                print(f"    Failed to fetch {url} after {retries} attempts: {e}")
                return None
            time.sleep(1.0)  # Wait before retry
        except json.JSONDecodeError:
            return None
    return None


# Batch size for story dialog translation (larger for better context)
STORY_BATCH_SIZE = 15  # Keep small for long dialog texts to avoid output token limits


def translate_event_story_episode_llm(
    api_key: str,
    jp_scenario: Dict[str, Any],
    llm_type: str,
    dry_run: bool = False
) -> Dict[str, str]:
    """
    Translate a single episode's TalkData using LLM.
    Returns a dict mapping JP text -> CN translation.
    """
    jp_talk_list = jp_scenario.get("TalkData", [])
    
    # Collect unique texts to translate (Body + WindowDisplayName)
    texts_to_translate = []
    seen = set()
    
    for talk in jp_talk_list:
        body = talk.get("Body", "")
        if body and body.strip() and body not in seen:
            texts_to_translate.append(body)
            seen.add(body)
        
        display_name = talk.get("WindowDisplayName", "")
        if display_name and display_name.strip() and display_name not in seen:
            texts_to_translate.append(display_name)
            seen.add(display_name)
    
    if not texts_to_translate:
        return {}
    
    talk_data: Dict[str, str] = {}
    
    # Translate in batches
    for i in range(0, len(texts_to_translate), STORY_BATCH_SIZE):
        batch = texts_to_translate[i:i + STORY_BATCH_SIZE]
        batch_num = i // STORY_BATCH_SIZE + 1
        total_batches = (len(texts_to_translate) - 1) // STORY_BATCH_SIZE + 1
        print(f"      LLM Batch {batch_num}/{total_batches} ({len(batch)} texts)")
        
        translations = translate_batch(api_key, batch, llm_type, dry_run, STORY_TRANSLATION_PROMPT)
        talk_data.update(translations)
        
        if not dry_run and i + STORY_BATCH_SIZE < len(texts_to_translate):
            time.sleep(RATE_LIMIT_DELAY)
    
    return talk_data


def extract_event_story_translations(
    dry_run: bool = False,
    force: bool = False,
    api_key: str = "",
    llm_type: str = "qwen",
    cn_only: bool = False
) -> None:
    """
    Extract event story translations from CN server scenario data,
    with LLM fallback for events without CN translations.
    Generates per-event translation files: eventStory/event_{eventId}.json
    
    Priority: CN official translation > LLM translation
    
    Args:
        dry_run: If True, do not save files
        force: If True, overwrite existing translation files
        api_key: LLM API key (required if not cn_only)
        llm_type: LLM type to use ("qwen" or "gemini")
        cn_only: If True, skip LLM translation
    """
    print(f"\n{'='*60}")
    print(f"Extracting Event Story Translations (CN + LLM Fallback)")
    print(f"{'='*60}")
    
    # Fetch event stories from both servers
    jp_stories = fetch_masterdata("eventStories.json", "jp")
    cn_stories = fetch_masterdata("eventStories.json", "cn")
    jp_events = fetch_masterdata("events.json", "jp")
    cn_events = fetch_masterdata("events.json", "cn")
    
    if not jp_stories:
        print("  Error: Could not fetch JP eventStories.json")
        return
    
    # Build lookup maps
    cn_story_by_event = {s["eventId"]: s for s in (cn_stories or [])}
    cn_event_ids = {e["id"] for e in (cn_events or [])}
    
    # Create output directory
    event_story_dir = OUTPUT_DIR / "eventStory"
    if not dry_run:
        event_story_dir.mkdir(parents=True, exist_ok=True)
    
    stats = {"cn_processed": 0, "llm_processed": 0, "episodes_processed": 0, "events_skipped": 0}
    
    # Collect events needing LLM translation (no CN data)
    events_needing_llm: List[Dict[str, Any]] = []
    
    for jp_story in jp_stories:
        event_id = jp_story["eventId"]
        output_file = event_story_dir / f"event_{event_id}.json"
        
        # --- Phase 1: CN Official Translation ---
        has_cn = event_id in cn_event_ids and event_id in cn_story_by_event
        
        if has_cn:
            cn_story = cn_story_by_event[event_id]
            
            # Check existing file for priority logic
            should_process = True
            if output_file.exists():
                try:
                    with open(output_file, "r", encoding="utf-8") as f:
                        existing_data = json.load(f)
                    
                    existing_source = existing_data.get("meta", {}).get("source", "official_cn")
                    
                    if existing_source == "llm":
                        print(f"  Event {event_id}: Overwriting LLM translation with Official CN data")
                        should_process = True
                    elif existing_source == "official_cn":
                        if force:
                            print(f"  Event {event_id}: Force updating existing Official CN data")
                            should_process = True
                        else:
                            should_process = False
                except Exception:
                    should_process = True
            
            if not should_process:
                stats["events_skipped"] += 1
                continue
            
            print(f"  [CN] Processing Event {event_id}: {jp_story['assetbundleName']}")
            
            episodes_translation: Dict[str, Any] = {}
            cn_episodes_map = {ep["episodeNo"]: ep for ep in cn_story.get("eventStoryEpisodes", [])}
            
            for jp_episode in jp_story.get("eventStoryEpisodes", []):
                episode_no = jp_episode["episodeNo"]
                scenario_id = jp_episode["scenarioId"]
                asset_bundle = jp_story["assetbundleName"]
                
                cn_episode = cn_episodes_map.get(episode_no)
                cn_title = cn_episode.get("title", "") if cn_episode else ""
                
                scenario_path = f"event_story/{asset_bundle}/scenario/{scenario_id}"
                
                cn_scenario = fetch_scenario_json(scenario_path)
                if not cn_scenario:
                    print(f"    Episode {episode_no}: CN scenario not found")
                    continue
                
                jp_scenario = fetch_scenario_json_from_url(f"{JP_ASSETS_URL}/{scenario_path}.json")
                
                talk_data: Dict[str, str] = {}
                cn_talk_list = cn_scenario.get("TalkData", [])
                
                for talk in cn_talk_list:
                    body = talk.get("Body", "")
                    if body and body.strip():
                        talk_data[body] = body
                    display_name = talk.get("WindowDisplayName", "")
                    if display_name and display_name.strip():
                        talk_data[display_name] = display_name
                
                if jp_scenario:
                    talk_data = {}
                    jp_talk_list = jp_scenario.get("TalkData", [])
                    for i_talk, (jp_talk, cn_talk) in enumerate(zip(jp_talk_list, cn_talk_list)):
                        jp_body = jp_talk.get("Body", "")
                        cn_body = cn_talk.get("Body", "")
                        if jp_body and cn_body and jp_body != cn_body:
                            talk_data[jp_body] = cn_body
                        jp_name = jp_talk.get("WindowDisplayName", "")
                        cn_name = cn_talk.get("WindowDisplayName", "")
                        if jp_name and cn_name and jp_name != cn_name:
                            talk_data[jp_name] = cn_name
                
                if talk_data:
                    episodes_translation[str(episode_no)] = {
                        "scenarioId": scenario_id,
                        "title": cn_title,
                        "talkData": talk_data
                    }
                    stats["episodes_processed"] += 1
                    print(f"    Episode {episode_no}: {len(talk_data)} translations" + (f", Title: {cn_title}" if cn_title else ""))
            
            if episodes_translation:
                final_data = {
                    "meta": {
                        "source": "official_cn",
                        "version": "1.0",
                        "last_updated": int(time.time())
                    },
                    "episodes": episodes_translation
                }
                if not dry_run:
                    with open(output_file, "w", encoding="utf-8") as f:
                        json.dump(final_data, f, ensure_ascii=False, indent=2)
                    print(f"    Saved to {output_file}")
                else:
                    print(f"    [DRY RUN] Would save to {output_file}")
                stats["cn_processed"] += 1
        else:
            # No CN data — candidate for LLM translation
            events_needing_llm.append(jp_story)
    
    # --- Phase 2: LLM Translation for events without CN data ---
    if events_needing_llm and cn_only:
        print(f"\n  {len(events_needing_llm)} events have no CN data (skipped, --cn-only mode)")
    elif events_needing_llm and not api_key:
        print(f"\n  {len(events_needing_llm)} events have no CN data (skipped, no API key)")
    elif events_needing_llm:
        print(f"\n  --- LLM Translation for {len(events_needing_llm)} events without CN data ---")
        
        for jp_story in events_needing_llm:
            event_id = jp_story["eventId"]
            output_file = event_story_dir / f"event_{event_id}.json"
            
            # Skip if already translated (unless force)
            if output_file.exists() and not force:
                try:
                    with open(output_file, "r", encoding="utf-8") as f:
                        existing_data = json.load(f)
                    existing_source = existing_data.get("meta", {}).get("source", "")
                    if existing_source:
                        stats["events_skipped"] += 1
                        continue
                except Exception:
                    pass
            
            print(f"  [LLM] Processing Event {event_id}: {jp_story['assetbundleName']}")
            
            episodes_translation = {}
            
            for jp_episode in jp_story.get("eventStoryEpisodes", []):
                episode_no = jp_episode["episodeNo"]
                scenario_id = jp_episode["scenarioId"]
                asset_bundle = jp_story["assetbundleName"]
                
                scenario_path = f"event_story/{asset_bundle}/scenario/{scenario_id}"
                jp_scenario = fetch_scenario_json_from_url(f"{JP_ASSETS_URL}/{scenario_path}.json")
                
                if not jp_scenario:
                    print(f"    Episode {episode_no}: JP scenario not found")
                    continue
                
                # Get JP title for LLM translation
                jp_title = jp_episode.get("title", "")
                cn_title = ""
                if jp_title:
                    title_translations = translate_batch(api_key, [jp_title], llm_type, dry_run, STORY_TRANSLATION_PROMPT)
                    cn_title = title_translations.get(jp_title, "")
                
                print(f"    Episode {episode_no}: Translating with LLM...")
                talk_data = translate_event_story_episode_llm(api_key, jp_scenario, llm_type, dry_run)
                
                if talk_data:
                    episodes_translation[str(episode_no)] = {
                        "scenarioId": scenario_id,
                        "title": cn_title,
                        "talkData": talk_data
                    }
                    stats["episodes_processed"] += 1
                    print(f"    Episode {episode_no}: {len(talk_data)} translations" + (f", Title: {cn_title}" if cn_title else ""))
                
                # Rate limit between episodes
                if not dry_run:
                    time.sleep(RATE_LIMIT_DELAY)
            
            if episodes_translation:
                final_data = {
                    "meta": {
                        "source": "llm",
                        "version": "1.0",
                        "last_updated": int(time.time())
                    },
                    "episodes": episodes_translation
                }
                if not dry_run:
                    with open(output_file, "w", encoding="utf-8") as f:
                        json.dump(final_data, f, ensure_ascii=False, indent=2)
                    print(f"    Saved to {output_file}")
                else:
                    print(f"    [DRY RUN] Would save to {output_file}")
                stats["llm_processed"] += 1
    
    print(f"\n  Summary: CN={stats['cn_processed']}, LLM={stats['llm_processed']}, Episodes={stats['episodes_processed']}, Skipped={stats['events_skipped']}")


# ============================================================================
# Search Index Generation
# ============================================================================

# Character names for search (mirrors web/src/types/types.ts CHARACTER_NAMES)
CHARACTER_NAMES = {
    1: "星乃一歌", 2: "天馬咲希", 3: "望月穂波", 4: "日野森志歩",
    5: "花里みのり", 6: "桐谷遥", 7: "桃井愛莉", 8: "日野森雫",
    9: "小豆沢こはね", 10: "白石杏", 11: "東雲彰人", 12: "青柳冬弥",
    13: "天馬司", 14: "鳳えむ", 15: "草薙寧々", 16: "神代類",
    17: "宵崎奏", 18: "朝比奈まふゆ", 19: "東雲絵名", 20: "暁山瑞希",
    21: "初音ミク", 22: "鏡音リン", 23: "鏡音レン", 24: "巡音ルカ",
    25: "MEIKO", 26: "KAITO",
}

# Character short names (Chinese, mirrors CHAR_NAMES in types.ts)
CHAR_NAMES_CN = {
    1: "一歌", 2: "咲希", 3: "穗波", 4: "志步",
    5: "实乃理", 6: "遥", 7: "爱莉", 8: "雫",
    9: "心羽", 10: "杏", 11: "彰人", 12: "冬弥",
    13: "司", 14: "笑梦", 15: "宁宁", 16: "类",
    17: "奏", 18: "真冬", 19: "绘名", 20: "瑞希",
    21: "Miku", 22: "Rin", 23: "Len", 24: "Luka", 25: "MEIKO", 26: "KAITO",
}


def generate_search_index(dry_run: bool = False):
    """Generate search-index.json for CommandPalette search.
    
    Fetches masterdata for cards, musics, events, and gachas,
    then combines with existing translations to produce a compact
    search index file.
    """
    print(f"\n{'='*60}")
    print(f"Generating Search Index")
    print(f"{'='*60}")
    
    index = []
    
    # Load existing translations for CN names
    cards_trans = load_existing_translations("cards")
    events_trans = load_existing_translations("events")
    gacha_trans = load_existing_translations("gacha")
    vl_trans = load_existing_translations("virtualLive")
    mysekai_trans = load_existing_translations("mysekai")
    costumes_trans = load_existing_translations("costumes")
    sticker_trans = load_existing_translations("sticker")

    def _get_cn(trans_data: Dict, field: str, jp_text: str) -> str:
        """Helper to extract CN translation text from translation data."""
        cn_entry = trans_data.get(field, {}).get(jp_text, {})
        if isinstance(cn_entry, dict):
            return cn_entry.get("text", "")
        elif isinstance(cn_entry, str):
            return cn_entry
        return ""
    
    # --- Events (priority 1) ---
    events = fetch_masterdata("events.json", "jp")
    if events:
        for event in events:
            entry: Dict[str, Any] = {
                "id": event["id"],
                "n": event.get("name", ""),
                "g": "events",
            }
            jp_name = event.get("name", "")
            cn_text = _get_cn(events_trans, "name", jp_name)
            if cn_text and cn_text != jp_name:
                entry["cn"] = cn_text
            if entry["n"]:
                index.append(entry)
        print(f"  Events: {len(events)} entries")
    
    # --- Music (priority 2) ---
    musics = fetch_masterdata("musics.json", "jp")
    if musics:
        for music in musics:
            entry = {
                "id": music["id"],
                "n": music.get("title", ""),
                "g": "music",
            }
            if entry["n"]:
                index.append(entry)
        print(f"  Music: {len(musics)} entries")
    
    # --- Cards (priority 3) ---
    cards = fetch_masterdata("cards.json", "jp")
    if cards:
        for card in cards:
            entry: Dict[str, Any] = {
                "id": card["id"],
                "n": card.get("prefix", ""),
                "g": "cards",
                "c": card.get("characterId", 0),
            }
            jp_prefix = card.get("prefix", "")
            cn_text = _get_cn(cards_trans, "prefix", jp_prefix)
            if cn_text and cn_text != jp_prefix:
                entry["cn"] = cn_text
            if entry["n"]:
                index.append(entry)
        print(f"  Cards: {len(cards)} entries")
    
    # --- Gacha (priority 4) ---
    gachas = fetch_masterdata("gachas.json", "jp")
    if gachas:
        for gacha in gachas:
            entry: Dict[str, Any] = {
                "id": gacha["id"],
                "n": gacha.get("name", ""),
                "g": "gacha",
            }
            jp_name = gacha.get("name", "")
            cn_text = _get_cn(gacha_trans, "name", jp_name)
            if cn_text and cn_text != jp_name:
                entry["cn"] = cn_text
            if entry["n"]:
                index.append(entry)
        print(f"  Gacha: {len(gachas)} entries")

    # --- Mysekai / 家具 (priority 5) ---
    fixtures = fetch_masterdata("mysekaiFixtures.json", "jp")
    if fixtures:
        count = 0
        for f in fixtures:
            entry: Dict[str, Any] = {
                "id": f["id"],
                "n": f.get("name", ""),
                "g": "mysekai",
            }
            jp_name = f.get("name", "")
            cn_text = _get_cn(mysekai_trans, "fixtureName", jp_name)
            if cn_text and cn_text != jp_name:
                entry["cn"] = cn_text
            if entry["n"]:
                index.append(entry)
                count += 1
        print(f"  Mysekai: {count} entries")

    # --- Costumes / 服装 (priority 6) ---
    costumes_data = fetch_masterdata("snowy_costumes.json", "jp")
    if costumes_data:
        costume_list = costumes_data.get("costumes", []) if isinstance(costumes_data, dict) else []
        count = 0
        for costume in costume_list:
            entry: Dict[str, Any] = {
                "id": costume["id"],
                "n": costume.get("name", ""),
                "g": "costumes",
            }
            jp_name = costume.get("name", "")
            cn_text = _get_cn(costumes_trans, "name", jp_name)
            if cn_text and cn_text != jp_name:
                entry["cn"] = cn_text
            if entry["n"] and entry["n"] != "-":
                index.append(entry)
                count += 1
        print(f"  Costumes: {count} entries")

    # --- Virtual Live / 演唱会 (priority 7) ---
    vlives = fetch_masterdata("virtualLives.json", "jp")
    if vlives:
        count = 0
        for vl in vlives:
            entry: Dict[str, Any] = {
                "id": vl["id"],
                "n": vl.get("name", ""),
                "g": "live",
            }
            jp_name = vl.get("name", "")
            cn_text = _get_cn(vl_trans, "name", jp_name)
            if cn_text and cn_text != jp_name:
                entry["cn"] = cn_text
            if entry["n"]:
                index.append(entry)
                count += 1
        print(f"  Virtual Live: {count} entries")
    
    print(f"  Total: {len(index)} entries")
    
    # Save to web/public/data/search-index.json
    output_path = PROJECT_ROOT / "web" / "public" / "data" / "search-index.json"
    if not dry_run:
        output_path.parent.mkdir(parents=True, exist_ok=True)
        with open(output_path, "w", encoding="utf-8") as f:
            json.dump(index, f, ensure_ascii=False, separators=(',', ':'))
        file_size = output_path.stat().st_size
        print(f"  Saved to {output_path} ({file_size / 1024:.1f} KB)")
    else:
        print(f"  [DRY RUN] Would save to {output_path}")


def main():
    parser = argparse.ArgumentParser(description="Translate Snowy Viewer masterdata to Chinese")
    parser.add_argument("--dry-run", action="store_true", help="Show what would be translated without calling API")
    parser.add_argument("--force", action="store_true", help="Overwrite existing translation files")
    parser.add_argument("--cn-only", action="store_true", help="Only use CN server translations, skip LLM")
    parser.add_argument("--llm", choices=["qwen", "gemini"], default="qwen", help="LLM to use (default: qwen)")
    parser.add_argument("--category", choices=["cards", "events", "music", "virtualLive", "gacha", "mysekai", "sticker", "comic", "costumes", "eventStory", "search-index"],
                        help="Translate only a specific category (or 'search-index' to regenerate search index)")
    args = parser.parse_args()
    
    api_key = ""
    if not args.dry_run and not args.cn_only:
        api_key = get_api_key(args.llm)
    
    print(f"LLM: {args.llm.upper()}")
    
    categories = {
        "cards": extract_cards_with_cn,
        "events": extract_events_with_cn,
        "music": extract_music_texts,
        "virtualLive": extract_virtual_live_with_cn,
        "gacha": extract_gacha_with_cn,
        "mysekai": extract_mysekai_with_cn,
        "sticker": extract_sticker_with_cn,
        "comic": extract_comic_with_cn,
        "characters": extract_characters_with_cn,
        "units": extract_units_with_cn,
        "costumes": extract_costumes_with_cn,
    }
    
    priority_order = ["cards", "events", "gacha", "virtualLive", "mysekai", "sticker", "comic", "characters", "units", "costumes", "music"]
    
    if args.category:
        if args.category == "eventStory":
            # Event story uses special handling (per-event files, with LLM fallback)
            extract_event_story_translations(args.dry_run, args.force, api_key, args.llm, args.cn_only)
        elif args.category == "search-index":
            generate_search_index(args.dry_run)
        else:
            translate_category_enhanced(api_key, args.category, categories[args.category], args.llm, args.dry_run, args.cn_only)
    else:
        for cat in priority_order:
            if cat in categories:
                translate_category_enhanced(api_key, cat, categories[cat], args.llm, args.dry_run, args.cn_only)
        # Also process event stories (with LLM fallback)
        extract_event_story_translations(args.dry_run, args.force, api_key, args.llm, args.cn_only)
        # Generate search index at the end of a full run
        generate_search_index(args.dry_run)
    
    print("\n" + "="*60)
    print("Translation complete!")
    print("="*60)


if __name__ == "__main__":
    main()
