# sekai-translate

Moesekai 翻译系统（Go 后端 + 校对 UI）

- 运行时翻译文件默认存储在持久卷 `TRANSLATION_PATH=/data/translations`
- 服务内每日定时执行 CN 同步（仅热更新文件，不自动推送）
- 备份通过手动触发 `POST /api/push` 推送到**本仓库独立分支**
- 不再依赖 `scripts/translate.py` 与 GitHub Action 定时任务

## 当前架构

```
sekai-translate/
├── main.go
├── backend/
│   ├── store.go        # 翻译存储与编辑
│   ├── handlers.go     # API
│   ├── translator.go   # Go 翻译引擎 (CN同步 + 手动AI)
│   ├── scheduler.go    # 每日定时任务
│   └── pusher.go       # git clone + push 备份
├── translations/
│   ├── *.json
│   ├── *.full.json
│   └── eventStory/
├── proofreading/       # Next.js 静态导出 UI
└── Dockerfile
```

## 运行参数

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `PORT` | `9090` | 服务端口 |
| `TRANSLATION_PATH` | `./translations`（Docker: `/data/translations`） | 翻译目录（建议挂载持久卷） |
| `STATIC_DIR` | `./proofreading/out` | 前端静态文件目录 |
| `TRANSLATOR_ACCOUNTS` | - | 登录账号 `user:pass,user2:pass2` |
| `AUTH_SECRET` | `sekai-translate-secret` | 鉴权密钥 |
| `TRANSLATE_SCHEDULER_ENABLED` | `true` | 开启每日定时任务 |
| `TRANSLATE_CRON_HOUR` | `4` | 每日 UTC 小时（4=UTC+8 中午12点） |
| `GIT_PUSH_REPO_URL` | - | 备份目标仓库 URL（建议带 token） |
| `GIT_PUSH_BRANCH` | `backup-translations` | 备份分支（建议与部署分支隔离） |
| `GIT_WORKSPACE` | `/app/git-workspace` | 容器 git 工作区 |
| `LLM_TYPE` | `gemini` | 默认 AI 提供方 (`gemini`/`openai`) |
| `GEMINI_API_KEY` | - | Gemini key |
| `GEMINI_MODEL` | `gemini-2.0-flash` | Gemini 模型 |
| `OPENAI_API_KEY` | - | OpenAI 兼容 key |
| `OPENAI_BASE_URL` | `https://api.openai.com/v1` | OpenAI 兼容端点 |
| `OPENAI_MODEL` | `gpt-4o-mini` | OpenAI 模型 |

## API

- `POST /api/login`
- `GET /api/categories`
- `GET /api/entries`
- `PUT /api/entry`
- `POST /api/push`（手动备份推送到备份分支）
- `GET /api/status`（推送状态）
- `GET /api/translate/status`（翻译/调度状态）
- `POST /api/translate/cn-sync`（手动触发 CN 同步，仅热更新）
- `POST /api/translate/ai`（手动 AI 翻译当前字段）
- `GET /api/event-stories`
- `GET /api/event-story?eventId=123`

## Docker

运行时镜像安装了 `git` 并预建 `/app/git-workspace`，适配 Zeabur 这类无 git 工作区平台。

默认行为：

- `TRANSLATION_PATH=/data/translations`（声明了 Docker volume）
- `GIT_PUSH_BRANCH=backup-translations`
- 容器首次启动且 `/data/translations` 为空时，会用镜像内 `./translations` 作为初始种子

## Zeabur 部署建议（避免编辑丢失）

1. 挂载持久卷到 `/data/translations`
2. 部署分支使用 `main`（或你的业务分支）
3. 备份分支使用 `backup-translations`（`GIT_PUSH_BRANCH=backup-translations`）
4. 确保 Zeabur 的自动部署不监听 `backup-translations`

推荐环境变量：

- `TRANSLATION_PATH=/data/translations`
- `GIT_PUSH_BRANCH=backup-translations`
- `GIT_PUSH_REPO_URL=https://x-access-token:<token>@github.com/<owner>/<repo>.git`
