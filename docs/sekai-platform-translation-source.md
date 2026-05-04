# SekaiPlatform 翻译源集成

## 1. 范围

`Moe_translation` 仅从 `SekaiPlatform` 获取（消费）翻译数据。

预期的创作流程为：

1. 译者使用 `SekaiPlatform` 和 `SekaiTools` 翻译文本。
2. `SekaiPlatform` 存储并提供翻译数据服务。
3. `Moe_translation` 从 `SekaiPlatform` 拉取翻译数据。
4. Moesekai 展示拉取到的翻译内容。

作者的署名权由 `SekaiPlatform` 管理。

## 2. 所需Platform能力

`Moe_translation` 需要 `SekaiPlatform` 提供只读 API。

最低限度所需的类别包括：

- 身份验证与租户选择
- 剧情故事查询
- 原文行查询
- 翻译版本查询
- 翻译行下载
- 可选的署名元数据

`Moe_translation` 不需要任何写入 API。

## 3. 身份验证与租户上下文

`SekaiPlatform` 的翻译具有租户作用域（租户隔离）。`Moe_translation` 必须能够进行身份验证，并选择需要获取其翻译数据的租户。

所需 API：

- `POST /api/auth/login`
- `GET /api/auth/session`
- `GET /api/auth/tenants`
- `PUT /api/auth/current-tenant`

预期行为：

- `Moe_translation` 存储或接收用于服务器端同步的访问令牌 (access token)。
- 请求翻译数据时使用 `Authorization: Bearer <token>` 标头。
- Platform从已验证的会话或选定的租户中解析出对应的租户。

待决问题：

- 生产环境的同步应使用普通用户令牌、服务账号还是专用机器令牌，需Platform方确认。

## 4. 剧情故事查询

`Moe_translation` 需要将 Moesekai 的剧情标识映射到Platform的剧情标识。

现有Platform设计的 API：

- `GET /api/story-groups`
- `GET /api/stories`
- `GET /api/stories/{storyId}`
- `GET /api/stories/{storyId}/source-lines`

`Moe_translation` 应当至少能通过以下字段定位剧情故事：

- `story_type`
- `scenario_id`

建议Platform方添加：

- `GET /api/stories:resolve`

建议的查询参数：

- `story_type`
- `scenario_id`

建议的响应字段：

- `story_id`
- `group_id`
- `story_type`
- `scenario_id`
- `title`

理由（设计依据）：

`Moe_translation` 和 Moesekai 通常掌握的是游戏端标识符（例如 `scenarioId`、`eventId` 和话数）。而Platform使用的是内部 ID（例如 `story_id`、`source_line_id` 和 `translation_version_id`）。提供一个解析（resolve）端点可以保持集成的简洁性，并避免脆弱的客户端搜索逻辑。

## 5. 原文行

所需 API：

- `GET /api/stories/{storyId}/source-lines`

所需的响应字段：

- `id` 或 `source_line_id`
- `story_id`
- `line_no`
- `line_type`
- `speaker`
- `text`
- `metadata`

理由：

`Moe_translation` 需要原文行以便进行对齐和展示。只有翻译行是不够的，因为客户端必须知道每一行翻译对应的是哪一行原文。

## 6. 翻译版本

所需 API：

- `GET /api/stories/{storyId}/translation-versions`
- `GET /api/translation-versions/{translationVersionId}`

所需的 `TranslationVersion` 字段：

- `id`
- `story_id`
- `version_no`
- `title`
- `created_at`
- `updated_at`

建议包含的字段：

- `language`
- `status`
- `line_count`
- `attribution`

在Platform最终确定作者模型之前，`attribution`（署名）的定义被刻意保持宽松。

署名数据结构示例：

```json
{
  "mode": "chapter",
  "display_text": "翻译：Alice / Bob",
  "contributors": [
    {
      "id": 1,
      "display_name": "Alice",
      "role": "translator"
    }
  ]
}
```

## 7. 翻译行

所需 API：

- `GET /api/translation-versions/{translationVersionId}/lines`

所需的响应字段：

- `id`
- `version_id`
- `source_line_id`
- `story_id`
- `line_no`
- `speaker`
- `text`
- `metadata`

建议包含的响应字段：

- `source_text`
- `line_type`
- `updated_at`
- `attribution`

如果Platform每行都返回署名信息，`Moe_translation` 应将其视为展示用的元数据，并避免在本地推断作者身份。

数据行示例：

```json
{
  "id": 456,
  "version_id": 123,
  "source_line_id": 789,
  "story_id": 10,
  "line_no": 1,
  "line_type": "dialogue",
  "speaker": "初音未来",
  "source_text": "こんにちは",
  "text": "你好",
  "metadata": {},
  "attribution": {
    "display_text": "翻译：Alice"
  }
}
```

## 8. 推荐的同步流程

1. 在Platform进行身份验证。
2. 选择或确认租户。
3. 将 Moesekai 的剧情标识解析为 `story_id`。
4. 获取该剧情的原文行。
5. 获取该剧情的翻译版本。
6. 选择要展示的版本。
7. 获取该版本的翻译行。
8. 通过 `source_line_id` 或 `line_no` 将翻译行与原文行合并。
9. 保留Platform提供的署名信息以供展示。

## 9. 版本选择

Platform应当定义 `Moe_translation` 应如何选择翻译版本。

可接受的方案：

- Platform将某个版本标记为默认或已发布。
- `Moe_translation` 配置一个首选的 `version_no`。
- Platform返回按优先级排序的版本列表，`Moe_translation` 使用第一个符合条件的版本。

用于版本选择的推荐响应字段：

- `status`
- `is_default`
- `published_at`
- `updated_at`

## 10. 缓存与刷新

`Moe_translation` 应该能够避免拉取未更改的翻译。

推荐Platform提供的支持：

- 翻译版本上的 `updated_at` 字段
- 翻译行上的 `updated_at` 字段
- 在可行的情况下提供 `ETag` 或 `Last-Modified` 标头

最低限度可运行的机制：

- `GET /api/translation-versions/{id}` 返回 `updated_at`。
- 如果 `updated_at` 未更改，`Moe_translation` 可以继续使用其缓存的行数据。

## 11. 明确的非目标（不在计划范围内的内容）

对于此次集成，`Moe_translation` 不需要以下Platform API：

- 创建翻译版本
- 更新翻译版本元数据
- 保存翻译行
- 导入翻译版本
- 管理译者
- 解析详细的作者历史记录

这些操作属于 `SekaiPlatform` 和 `SekaiTools` 的职责范围。

## 12. 向Platform方提出的待决问题

以下几点需要Platform方做出决定：

1. 服务器端的 `Moe_translation` 应该使用什么身份验证方法？
2. 应该如何选择默认或已发布的翻译版本？
3. 署名是版本级别、章节级别、行级别，还是混合式的？
4. 署名信息将随 `TranslationVersion` 返回，随 `TranslationLine` 返回，还是通过一个独立的端点返回？
5. Platform能否提供 `GET /api/stories:resolve` 以便通过 `story_type + scenario_id` 进行查询？
6. 翻译行的响应中是否会包含 `source_text`（原文），还是 `Moe_translation` 应该始终自行与原文行进行合并？

## 13. 最低 API 需求清单

对于首次只读集成，`SekaiPlatform` 应提供：

- `POST /api/auth/login`
- `GET /api/auth/session`
- `GET /api/auth/tenants`
- `PUT /api/auth/current-tenant`
- `GET /api/stories`
- `GET /api/stories/{storyId}/source-lines`
- `GET /api/stories/{storyId}/translation-versions`
- `GET /api/translation-versions/{translationVersionId}`
- `GET /api/translation-versions/{translationVersionId}/lines`

强烈推荐提供：

- `GET /api/stories:resolve`