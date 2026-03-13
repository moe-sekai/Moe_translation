#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
为现有的活动剧情数据添加角色名称信息

该脚本会：
1. 读取所有 eventStory/*.json 文件
2. 从日服获取原始剧情数据
3. 提取每行对话对应的角色名称
4. 更新 JSON 文件，添加 speakerNames 字段
"""

import json
import os
import sys
import time
import requests
from pathlib import Path

# 设置 Windows 控制台编码
if sys.platform == "win32":
    import codecs
    sys.stdout = codecs.getwriter("utf-8")(sys.stdout.buffer, "strict")
    sys.stderr = codecs.getwriter("utf-8")(sys.stderr.buffer, "strict")

# 日服资源 URL
JP_ASSETS_URL = "https://snowyassets.exmeaning.com/ondemand"
JP_ASSETS_FALLBACK_URL = "https://assets.unipjsk.com/ondemand"

def fetch_json(url):
    """获取 JSON 数据"""
    try:
        resp = requests.get(url, timeout=30)
        resp.raise_for_status()
        return resp.json()
    except Exception as e:
        print(f"  ⚠️  获取失败: {url} - {e}")
        return None

def fetch_jp_scenario(asset_path):
    """获取日服剧情数据，支持回退"""
    primary_url = f"{JP_ASSETS_URL}/{asset_path}.json"
    data = fetch_json(primary_url)
    if data and data.get("TalkData"):
        return data
    
    # 尝试回退 URL
    fallback_url = f"{JP_ASSETS_FALLBACK_URL}/{asset_path}.json"
    print(f"  ⚠️  主源无数据，尝试备用源...")
    data = fetch_json(fallback_url)
    if data and data.get("TalkData"):
        return data
    
    return None

def extract_speaker_names(jp_scenario):
    """从日服剧情数据中提取角色名称映射"""
    speaker_names = {}
    talk_data = jp_scenario.get("TalkData", [])
    
    for talk in talk_data:
        body = (talk.get("Body") or "").strip()
        speaker = (talk.get("WindowDisplayName") or "").strip()
        
        if body and speaker:
            speaker_names[body] = speaker
    
    return speaker_names

def process_event_story(event_file):
    """处理单个活动剧情文件"""
    event_id = event_file.stem.replace("event_", "")
    print(f"\n处理 Event {event_id}...")
    
    # 读取现有数据
    with open(event_file, "r", encoding="utf-8") as f:
        data = json.load(f)
    
    episodes = data.get("episodes", {})
    if not episodes:
        print(f"  ⚠️  没有章节数据")
        return False
    
    # 检查是否已有 speakerNames
    has_speaker_names = any(
        ep.get("speakerNames") for ep in episodes.values()
    )
    if has_speaker_names:
        print(f"  ✓ 已有角色名称数据，跳过")
        return False
    
    updated = False
    
    for ep_no, episode in episodes.items():
        scenario_id = episode.get("scenarioId")
        if not scenario_id:
            continue
        
        # 从 scenarioId 推断 asset bundle 名称
        # 例如: event_01_01 -> event_01
        asset_bundle = "_".join(scenario_id.split("_")[:2])
        scenario_path = f"event_story/{asset_bundle}/scenario/{scenario_id}"
        
        print(f"  章节 {ep_no}: {scenario_id}")
        
        # 获取日服剧情数据
        jp_scenario = fetch_jp_scenario(scenario_path)
        if not jp_scenario:
            print(f"    ⚠️  无法获取日服数据")
            continue
        
        # 提取角色名称
        speaker_names = extract_speaker_names(jp_scenario)
        if not speaker_names:
            print(f"    ⚠️  没有角色名称数据")
            continue
        
        # 更新数据
        episode["speakerNames"] = speaker_names
        updated = True
        print(f"    ✓ 添加了 {len(speaker_names)} 个角色名称")
        
        # 避免请求过快
        time.sleep(0.5)
    
    if updated:
        # 更新时间戳
        if "meta" in data:
            data["meta"]["last_updated"] = int(time.time())
        
        # 保存文件
        with open(event_file, "w", encoding="utf-8") as f:
            json.dump(data, f, ensure_ascii=False, indent=2)
        
        print(f"  ✓ 已保存更新")
        return True
    
    return False

def main():
    # 获取 translations/eventStory 目录
    script_dir = Path(__file__).parent
    project_dir = script_dir.parent
    event_story_dir = project_dir / "translations" / "eventStory"
    
    if not event_story_dir.exists():
        print(f"错误: 目录不存在: {event_story_dir}")
        sys.exit(1)
    
    # 获取所有活动剧情文件
    event_files = sorted(event_story_dir.glob("event_*.json"))
    event_files = [f for f in event_files if ".full." not in f.name]
    
    if not event_files:
        print("错误: 没有找到活动剧情文件")
        sys.exit(1)
    
    # 限制处理数量（用于测试）
    import argparse
    parser = argparse.ArgumentParser(description="为活动剧情添加角色名称")
    parser.add_argument("--limit", type=int, default=None, help="限制处理的文件数量")
    parser.add_argument("--start", type=int, default=1, help="从第几个活动开始")
    args = parser.parse_args()
    
    if args.start > 1:
        event_files = [f for f in event_files if int(f.stem.replace("event_", "")) >= args.start]
    
    if args.limit:
        event_files = event_files[:args.limit]
    
    print(f"找到 {len(event_files)} 个活动剧情文件")
    
    # 处理每个文件
    updated_count = 0
    for event_file in event_files:
        try:
            if process_event_story(event_file):
                updated_count += 1
        except KeyboardInterrupt:
            print("\n\n中断！")
            break
        except Exception as e:
            print(f"  错误: 处理失败: {e}")
            import traceback
            traceback.print_exc()
    
    print(f"\n完成！更新了 {updated_count} 个文件")

if __name__ == "__main__":
    main()
