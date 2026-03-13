# 活动剧情角色名称功能

## 功能说明

为活动剧情的每一行对话添加了说话人（角色名称）的显示功能。

## 实现细节

### 后端修改

1. **数据结构扩展** (`backend/translator.go`)
   - `EventStoryEpisode` 添加 `SpeakerNames map[string]string` 字段
   - `eventStoryEpisodePayload` 添加 `SpeakerNames` 字段

2. **数据提取逻辑** (`backend/translator.go`)
   - `buildOfficialCNEpisodes`: 从日服和国服剧情数据中提取 `WindowDisplayName`（角色名称）
   - `buildJPPendingEpisodes`: 从日服剧情数据中提取角色名称
   - 角色名称以日文对话文本为 key 存储在 `speakerNames` 映射中

### 前端修改

1. **类型定义** (`proofreading/src/lib/api.ts`)
   - `TranslationEntry` 添加 `speakerName?: string` 字段
   - `EventStoryDetail.episodes` 添加 `speakerNames?: Record<string, string>` 字段

2. **数据处理** (`proofreading/src/app/client.tsx`)
   - `buildEventStoryEntries`: 从剧情数据中提取角色名称并附加到每个条目
   - 在编辑面板和列表视图中显示角色名称

3. **UI 展示**
   - 编辑面板：在日文原文上方显示角色名称（蓝色加粗）
   - 列表视图：在每行对话上方显示角色名称（蓝色加粗）

## 数据格式示例

```json
{
  "meta": {
    "source": "official_cn",
    "version": "1.0",
    "last_updated": 1771555511
  },
  "episodes": {
    "1": {
      "scenarioId": "event_01_01",
      "title": "孤独的雨",
      "talkData": {
        "おーい咲希！　まだ支度しているのかー？": "喂，咲希！你还没收拾好吗？",
        "司の声": "司的声音"
      },
      "speakerNames": {
        "おーい咲希！　まだ支度しているのかー？": "司"
      }
    }
  }
}
```

## 数据迁移

### 自动迁移（推荐）

运行 CN 同步功能会自动为新数据添加角色名称：

```bash
# 通过 API 触发
POST /api/translate/cn-sync

# 或通过前端界面的"CN 同步"按钮
```

### 手动迁移脚本

如果需要为现有数据批量添加角色名称，可以使用迁移脚本：

```bash
# 处理所有文件
python scripts/add_speaker_names.py

# 只处理前 10 个活动
python scripts/add_speaker_names.py --limit 10

# 从第 50 个活动开始处理
python scripts/add_speaker_names.py --start 50

# 处理第 50-60 个活动
python scripts/add_speaker_names.py --start 50 --limit 10
```

**注意**: 
- 脚本需要访问日服资源服务器，可能需要较长时间
- 建议分批处理，避免网络超时
- 已有 `speakerNames` 的文件会自动跳过

## 兼容性

- 旧数据（没有 `speakerNames` 字段）仍然可以正常显示，只是不会显示角色名称
- 新数据会自动包含角色名称信息
- 前端会优雅地处理缺失的角色名称数据

## 测试

1. 编译后端：
   ```bash
   cd backend
   go build
   ```

2. 编译前端：
   ```bash
   cd proofreading
   npm run build
   ```

3. 运行服务并访问活动剧情页面，查看角色名称是否正确显示

## 未来改进

- [ ] 支持自定义角色名称翻译
- [ ] 在搜索功能中支持按角色名称筛选
- [ ] 添加角色名称的统计信息
