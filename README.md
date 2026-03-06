# sekai-translate

Moesekai 翻译系统 — 轻量后端 + 校对 UI + 自动推送到静态仓库。

## 架构

```
sekai-translate/
├── main.go                # Go 服务入口
├── backend/               # 轻量后端 (store + auth + pusher + handlers)
├── translations/          # 翻译 JSON 数据 (source of truth)
│   ├── *.json             # 扁平格式 (前端消费)
│   ├── *.full.json        # 完整格式 (带 source 追踪)
│   └── eventStory/        # 活动剧情翻译
├── proofreading/          # 校对 UI (Next.js CSR 静态导出)
├── scripts/translate.py   # 翻译生成脚本 (CN对照 + LLM)
├── Dockerfile             # 多阶段构建: Go + Node → alpine
└── .github/workflows/     # 自动翻译 (每6h)
```

### 数据流

```
translate.py → translations/*.json (本地) → Go 后端管理
                                            ├─ 校对 UI 读写 (API)
                                            └─ 推送到 MoeSekai-Hub (GitHub API)
                                                 └─ moe.exmeaning.com/translation/*.json
                                                      └─ 主站 pjsk.moe 前端获取
```

## 部署

```bash
docker build -t sekai-translate .
docker run -p 9090:9090 \
  -e TRANSLATOR_ACCOUNTS="user1:pass1,user2:pass2" \
  -e GITHUB_TOKEN="ghp_xxx" \
  -e GITHUB_REPO="moe-sekai/MoeSekai-Hub" \
  -e AUTO_PUSH_ENABLED=true \
  sekai-translate
```

环境变量:
| 变量 | 默认值 | 说明 |
|------|--------|------|
| `PORT` | `9090` | 服务端口 |
| `TRANSLATION_PATH` | `./translations` | 翻译数据目录 |
| `TRANSLATOR_ACCOUNTS` | - | 校对账号 `user:pass,...` |
| `AUTH_SECRET` | `sekai-translate-secret` | Token 签名密钥 |
| `GITHUB_TOKEN` | - | GitHub PAT (push 用) |
| `GITHUB_REPO` | `moe-sekai/MoeSekai-Hub` | 推送目标仓库 |
| `GITHUB_PUSH_PATH` | `translation` | 仓库内路径 |
| `GITHUB_BRANCH` | `main` | 推送分支 |
| `AUTO_PUSH_ENABLED` | `false` | 开启定时推送 (1h) |
| `STATIC_DIR` | `./proofreading/out` | 校对 UI 静态文件目录 |

URL:
- `/api/*` → 校对 API
- `/translation/editor/` → 校对 UI

## 翻译数据格式

### 扁平格式 (*.json) — 前端消费
```json
{
  "prefix": {
    "日本語テキスト": "中文翻译"
  }
}
```

### 完整格式 (*.full.json) — 内部追踪
```json
{
  "prefix": {
    "日本語テキスト": {
      "text": "中文翻译",
      "source": "cn"
    }
  }
}
```

Source 类型: `cn` (官方) | `human` (人工校对) | `pinned` (锁定) | `llm` (AI) | `unknown`

## 校对工具

校对 UI 为 Next.js 静态导出 (CSR)，通过 Go 后端 API 读写翻译数据:

1. 使用账号密码登录 → 后端返回 token
2. 浏览翻译分类/字段，按来源过滤
3. 编辑翻译条目，Enter 保存并跳转下一条
4. 点击「推送到 Hub」→ 后端通过 GitHub API 更新 MoeSekai-Hub

快捷键: `Enter` 保存 | `Ctrl+↑↓` 切换条目 | `j/k` 上下导航 | `Esc` 取消

## 翻译生成

```bash
# 仅 CN 服对照翻译
python scripts/translate.py --cn-only

# CN + LLM (Gemini) 自动翻译
python scripts/translate.py --llm gemini

# 指定单个类别
python scripts/translate.py --category cards --llm gemini
```

GitHub Actions 每 6 小时自动运行翻译脚本。

## 主站集成

主站 (pjsk.moe) 从静态 CDN 获取翻译数据:

```
https://moe.exmeaning.com/translation/cards.json
https://moe.exmeaning.com/translation/events.json
...
```

完全解耦：翻译更新 → 推送到 MoeSekai-Hub → 自动部署 → 主站无需重新构建。
