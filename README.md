# GitHub PR 智能代码审查 Agent

一个基于 Go 的 GitHub PR 自动审查 Agent。收到 GitHub PR 事件后，自动读取 PR 上下文和 diff，调用 DeepSeek 生成审查意见，并以 GitHub App bot 身份回写 PR Review。

## 当前进度

MVP 已经跑通并部署到 Railway：

- 接收 GitHub Webhook
- 校验 `X-Hub-Signature-256`
- 只接受 `pull_request` 事件，并过滤有效 action
- 使用 GitHub App installation token 访问 GitHub API
- 读取 PR 信息、diff 和变更文件内容，PR 文件列表支持分页
- PR 没有变更文件时直接回固定评论，不再调用 LLM
- 调用 DeepSeek 生成结构化 JSON 审查结果
- 解析并保存 `summary`、`findings`、模型、token 用量和 LLM 耗时
- 通过 PR Review API 回写 `COMMENT` 类型审查
- MySQL `review_task` 表记录任务状态，`review_result` 表保存完整审查结果
- 通过 `delivery_id` 唯一约束实现 Webhook 幂等，重复投递不会重复审查
- 支持优雅停机，退出前等待正在执行的审查任务
- 提供 `/tasks`、`/tasks/:id` 查询任务状态，以及 `/tasks/:id/result` 查询结构化审查结果

当前线上示例：

```text
https://github-pr-review-agent-production.up.railway.app
```

健康检查：

```text
GET /healthz
```

## GitHub App 配置

创建 GitHub App 时需要：

- Webhook URL：
  ```text
  https://github-pr-review-agent-production.up.railway.app/webhook/github
  ```
- Webhook secret：和 Railway 里的 `GITHUB_WEBHOOK_SECRET` 保持一致
- Subscribe to events：`Pull request`

需要的仓库权限：

| 权限 | 级别 | 用途 |
| --- | --- | --- |
| `Pull requests` | `Read & write` | 读取 PR / diff，提交 PR Review |
| `Contents` | `Read-only` | 读取仓库文件内容 |
| `Metadata` | `Read-only` | 基础仓库信息 |

不再需要 `Issues: write`。当前实现使用 PR Review API，不是 Issue Comment API。

## 环境变量

Railway / 生产环境建议使用 GitHub App：

```text
PORT=8080
APP_ENV=production
GITHUB_WEBHOOK_SECRET=...
GITHUB_APP_ID=...
GITHUB_APP_PRIVATE_KEY=...
GITHUB_INSTALLATION_ID=...
DEEPSEEK_API_KEY=...
DEEPSEEK_BASE_URL=https://api.deepseek.com
DEEPSEEK_MODEL=deepseek-chat
MAX_DIFF_LINES=2000
MAX_FILE_CONTEXTS=10
MAX_FILE_CONTEXT_LINES=200
MYSQL_DSN=root:<password>@tcp(127.0.0.1:3306)/github_pr_review_agent?charset=utf8mb4&parseTime=true&loc=Local
ADMIN_TOKEN=...
```

说明：

- `GITHUB_APP_PRIVATE_KEY`：GitHub App 私钥内容，适合 Railway。
- `APP_ENV`：`production` 时启动强制要求 `GITHUB_WEBHOOK_SECRET` 和 `ADMIN_TOKEN`；本地默认 `local`，允许为空方便调试。
- `GITHUB_APP_PRIVATE_KEY_PATH`：私钥文件路径，适合本地调试。
- `GITHUB_INSTALLATION_ID`：GitHub App 安装到仓库后的 installation ID。
- `MAX_DIFF_LINES`：控制送给 LLM 的 diff 长度，避免 token 成本过高。
- `MAX_FILE_CONTEXTS`：最多读取几个变更文件的完整内容，`0` 表示关闭。
- `MAX_FILE_CONTEXT_LINES`：每个文件最多送多少行内容给 LLM。
- `MYSQL_DSN`：MySQL 连接串。不要把真实密码提交进 Git。
- `ADMIN_TOKEN`：查询任务接口的 Bearer Token；为空时接口不鉴权，生产环境建议配置。

注意：Railway 容器无法通过 `127.0.0.1` 访问你本机的 MySQL。线上要在 Railway 里创建 MySQL 服务，或使用其他公网可达的 MySQL，并把对应的 `MYSQL_DSN` 配到 Railway 变量里。

本地快速调试也可以用 PAT：

```text
GITHUB_TOKEN=...
```

但正式演示和部署建议使用 GitHub App，这样评论会以独立 bot 身份发出。

## 本地运行

1. 准备环境变量：

   ```powershell
   $env:GITHUB_WEBHOOK_SECRET="你的 webhook secret"
   $env:GITHUB_APP_ID="你的 App ID"
   $env:GITHUB_APP_PRIVATE_KEY_PATH="D:\path\to\private-key.pem"
   $env:GITHUB_INSTALLATION_ID="你的 installation ID"
$env:DEEPSEEK_API_KEY="你的 DeepSeek API key"
$env:MYSQL_DSN="root:<password>@tcp(127.0.0.1:3306)/github_pr_review_agent?charset=utf8mb4&parseTime=true&loc=Local"
$env:ADMIN_TOKEN="本地调试 token，可不配"
```

2. 启动服务：

   ```powershell
   go run ./cmd/server
   ```

3. 如需本地接收 GitHub Webhook，再用 ngrok 临时暴露端口：

   ```powershell
   ngrok http 8080
   ```

   然后把 GitHub App 的 webhook URL 临时改成：

   ```text
   https://<你的-ngrok-域名>/webhook/github
   ```

ngrok 只用于本地调试；线上部署使用 Railway 的公网 HTTPS 地址，不需要 ngrok。

## 测试

```powershell
go test ./...
go vet ./...
```

## 任务状态查询

服务启动后会自动创建 `review_task` 表。任务状态流转：

```text
received -> running -> done
                 |
                 v
               failed
```

查询任务列表：

```text
GET /tasks?repo=Lhh220/github-pr-review-agent&status=done&limit=20
Authorization: Bearer <ADMIN_TOKEN>
```

列表和详情会返回 `duration_ms`，表示从任务创建到最后一次状态更新的耗时。任务仍在运行时，该值是到最近一次状态更新的耗时；任务完成后是总耗时。

查询单个任务：

```text
GET /tasks/<task_id>
Authorization: Bearer <ADMIN_TOKEN>
```

返回示例：

```json
{
  "id": 1,
  "repo": "Lhh220/github-pr-review-agent",
  "pr_number": 7,
  "commit_sha": "0123456789abcdef0123456789abcdef01234567",
  "action": "opened",
  "status": "done",
  "duration_ms": 3200
}
```

查询任务审查结果：

```text
GET /tasks/<task_id>/result
Authorization: Bearer <ADMIN_TOKEN>
```

返回示例：

```json
{
  "task": {
    "id": 1,
    "status": "done"
  },
  "result": {
    "id": 1,
    "task_id": 1,
    "summary": "No blocking issues.",
    "findings": [
      {
        "category": "bug",
        "file": "internal/review/service.go",
        "line": 12,
        "severity": "medium",
        "comment": "Example finding.",
        "suggestion": "Example suggestion.",
        "confidence": "confirmed"
      }
    ],
    "model": "deepseek-chat",
    "input_tokens": 1000,
    "output_tokens": 200,
    "total_tokens": 1200,
    "llm_duration_ms": 2500
  }
}
```

说明：

- `findings` 是结构化问题列表，字段包括 `category / file / line / severity / comment / suggestion / confidence`。
- `raw_response` 保留模型原始输出，方便排查模型偶发不按 JSON 返回的情况。
- `input_tokens / output_tokens / total_tokens` 来自 DeepSeek 返回的 usage，用于成本统计。
- `llm_duration_ms` 是单次 LLM 调用耗时。

PR 审查评论末尾会附带任务标识，例如：

```text
Task ID: 1 | commit 291ac5a
```

可以通过这个任务 ID 到 `/tasks/:id` 查询完整状态。

## 当前能力边界

当前 MVP 是 **diff + changed-file-context reviewer**：

- 会把 PR diff 和变更文件内容交给 LLM
- 还看不到改动文件之外的关联代码
- 无法确认被删除的字段、函数、类型是否仍被其他文件引用
- 无法验证 PR 是否能通过编译、测试或静态检查

下一阶段计划升级为 **code-aware agent**，增加：

- `read_file_context`
- `search_references`
- `run_static_checks`
- `confirmed / needs_verification` 结论分级

详细设计见 [docs/design.md](docs/design.md)。

## 后续计划

- RabbitMQ 异步队列
- Redis 分布式锁和限流
- MySQL 任务、结果、审计存储
- Tool Calling 框架
- tree-sitter 上下文裁剪
- 评测集和误报率统计

开发节奏见 [docs/roadmap.md](docs/roadmap.md)。
