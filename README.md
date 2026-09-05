# GitHub PR 智能代码审查 Agent

一个基于 Go 的 GitHub PR 自动审查 Agent。收到 GitHub PR 事件后，任务进入 RabbitMQ 异步队列，由 Worker 读取 PR 上下文和 diff，调用 DeepSeek 生成结构化审查意见，并以 GitHub App bot 身份回写 PR Review。

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
- Webhook 创建任务后投递 RabbitMQ，快速返回 202
- RabbitMQ 使用 durable queue、persistent message、publisher confirm 和 manual ack
- RabbitMQ 连接断开后自动重连，broker 短暂重启时 Go 服务保持在线
- 业务失败进入延迟重试队列，按指数退避回到主队列
- 重试延迟附加随机 jitter，避免失败任务同时重试
- 超过最大重试次数后进入死信队列，任务状态标记为 `dead_letter`
- 定期扫描超时仍处于 `running` 的任务，自动恢复到重试链路
- 定期扫描超时仍处于 `queued` 的任务，自动重新投递，避免进程崩溃导致任务滞留
- Worker Pool 固定并发消费任务，支持优雅停机
- Redis PR 级分布式锁，避免同一个 PR 的不同任务被并发审查
- Redis 固定窗口限流，分别保护 GitHub API 和 DeepSeek API
- 任务状态覆盖 `received -> queued -> running -> done/failed/retrying/dead_letter`
- 提供 `/tasks`、`/tasks/:id` 查询任务状态，以及 `/tasks/:id/result` 查询结构化审查结果
- 提供 `/dead-letters` 查询死信任务，`/dead-letters/:id/requeue` 手动重新入队
- MySQL 结构通过版本化 migration 管理，服务启动自动执行，也提供 `cmd/migrate` CLI

Day 4 的 Redis 锁和限流代码已完成并通过单元测试；需要在 Railway 增加 Redis 服务并配置 `REDIS_URL` 后完成线上验收。

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
RABBITMQ_URL=amqp://guest:guest@127.0.0.1:5672/
REVIEW_QUEUE=pr.review.queue
REVIEW_RETRY_QUEUE=pr.review.retry.queue
REVIEW_DEAD_LETTER_QUEUE=pr.review.dead_letter.queue
REVIEW_WORKERS=4
REVIEW_MAX_ATTEMPTS=3
REVIEW_RETRY_BASE_DELAY=30s
REVIEW_RETRY_MAX_DELAY=10m
REVIEW_RETRY_JITTER=5s
REDIS_URL=redis://127.0.0.1:6379/0
REVIEW_LOCK_TTL=7m
REVIEW_LOCK_RETRY_DELAY=2s
GITHUB_API_RATE_LIMIT=120
GITHUB_API_RATE_WINDOW=1m
LLM_RATE_LIMIT=6
LLM_RATE_WINDOW=1m
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
- `RABBITMQ_URL`：生产环境必填。本地未配置时默认使用 `amqp://guest:guest@127.0.0.1:5672/`。
- `REVIEW_QUEUE`：RabbitMQ durable 队列名，默认 `pr.review.queue`。
- `REVIEW_RETRY_QUEUE`：延迟重试队列，默认 `pr.review.retry.queue`。消息带 TTL，过期后通过 DLX 回到主队列。
- `REVIEW_DEAD_LETTER_QUEUE`：死信队列，默认 `pr.review.dead_letter.queue`。
- `REVIEW_WORKERS`：Worker 并发数，默认 4；RabbitMQ consumer prefetch 会使用同一配置。
- `REVIEW_MAX_ATTEMPTS`：最大执行次数，默认 3。前两次失败重试，第 3 次失败进入死信。
- `REVIEW_RETRY_BASE_DELAY`：第一次重试延迟，默认 30s。
- `REVIEW_RETRY_MAX_DELAY`：单次重试延迟上限，默认 10m。
- `REVIEW_RETRY_JITTER`：每次重试附加的随机延迟上限，默认 5s；设置为 `0s` 可关闭。
- `REDIS_URL`：Redis 连接串。生产环境必填；本地未配置时默认使用 `redis://127.0.0.1:6379/0`。
- `REVIEW_LOCK_TTL`：PR 级分布式锁 TTL，默认 7m，大于单次审查超时 5m，进程崩溃后锁会自动过期。
- `REVIEW_LOCK_RETRY_DELAY`：同一个 PR 已有审查在执行时，后续任务重新入队等待的延迟，默认 2s。
- `GITHUB_API_RATE_LIMIT / GITHUB_API_RATE_WINDOW`：GitHub API 限流，默认 120 次 / 1m。
- `LLM_RATE_LIMIT / LLM_RATE_WINDOW`：DeepSeek 调用限流，默认 6 次 / 1m，用于控制成本和上游压力。

注意：阶段二接入 RabbitMQ 后，Railway 部署必须提供可达的 `RABBITMQ_URL`，否则服务启动会失败。

注意：Day 4 接入 Redis 后，Railway 部署必须提供可达的 `REDIS_URL`，否则服务启动会失败。

注意：Railway 容器无法通过 `127.0.0.1` 访问你本机的 MySQL。线上要在 Railway 里创建 MySQL 服务，或使用其他公网可达的 MySQL，并把对应的 `MYSQL_DSN` 配到 Railway 变量里。

本地快速调试也可以用 PAT：

```text
GITHUB_TOKEN=...
```

但正式演示和部署建议使用 GitHub App，这样评论会以独立 bot 身份发出。

## 本地运行

1. 启动本地 RabbitMQ 和 Redis：

   ```powershell
   docker compose up -d rabbitmq redis
   ```

   管理界面：

   ```text
   http://127.0.0.1:15672
   ```

   默认账号密码是 `guest / guest`，仅本地可用。

2. 查看数据库 migration 状态：

   ```powershell
   $env:MYSQL_DSN="root:<password>@tcp(127.0.0.1:3306)/github_pr_review_agent?charset=utf8mb4&parseTime=true&loc=Local"
   go run ./cmd/migrate status
   go run ./cmd/migrate up
   ```

   服务启动时也会自动执行 migration。

3. 准备环境变量：

   ```powershell
   $env:GITHUB_WEBHOOK_SECRET="你的 webhook secret"
   $env:GITHUB_APP_ID="你的 App ID"
   $env:GITHUB_APP_PRIVATE_KEY_PATH="D:\path\to\private-key.pem"
   $env:GITHUB_INSTALLATION_ID="你的 installation ID"
$env:DEEPSEEK_API_KEY="你的 DeepSeek API key"
$env:MYSQL_DSN="root:<password>@tcp(127.0.0.1:3306)/github_pr_review_agent?charset=utf8mb4&parseTime=true&loc=Local"
$env:ADMIN_TOKEN="本地调试 token，可不配"
$env:RABBITMQ_URL="amqp://guest:guest@127.0.0.1:5672/"
$env:REVIEW_QUEUE="pr.review.queue"
$env:REVIEW_RETRY_QUEUE="pr.review.retry.queue"
$env:REVIEW_DEAD_LETTER_QUEUE="pr.review.dead_letter.queue"
$env:REVIEW_WORKERS="4"
$env:REVIEW_MAX_ATTEMPTS="3"
$env:REVIEW_RETRY_BASE_DELAY="30s"
$env:REVIEW_RETRY_MAX_DELAY="10m"
$env:REDIS_URL="redis://127.0.0.1:6379/0"
```

4. 启动服务：

   ```powershell
   go run ./cmd/server
   ```

5. 如需本地接收 GitHub Webhook，再用 ngrok 临时暴露端口：

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
received -> queued -> running -> done
                        |
                        v
                    retrying -> running -> ...
                        |
                        v
                   dead_letter

running -> failed   # 队列发布失败等不可重试的基础设施错误
```

重试语义：

- `attempt_count` 记录已执行次数。
- `max_attempts` 是最大执行次数，默认 3。
- `next_retry_at` 是下次允许执行的时间。
- 第 1 次失败延迟 30s + jitter，第 2 次失败延迟 60s + jitter，第 3 次失败进入死信队列。
- 延迟由消息 TTL 实现：`pr.review.retry.queue` 中的消息过期后，通过 DLX 自动回到 `pr.review.queue`。
- Worker 每 30 秒扫描一次超过 6 分钟仍是 `running` 的任务，避免进程崩溃后任务卡死。
- Worker 每 30 秒扫描一次超过 60 秒仍是 `queued` 的任务，自动重新投递；重复消息由原子 claim 和状态机兜底。

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
  "attempt_count": 1,
  "max_attempts": 3,
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

查询死信任务：

```text
GET /dead-letters?repo=Lhh220/github-pr-review-agent&limit=20
Authorization: Bearer <ADMIN_TOKEN>
```

手动把死信任务重新入队：

```text
POST /dead-letters/<task_id>/requeue
Authorization: Bearer <ADMIN_TOKEN>
```

Requeue 会把任务从 `dead_letter` 改回 `queued`，重置 `attempt_count`，并投递到主队列。即使投递 RabbitMQ 失败，任务也会由 queued 超时兜底恢复机制重新投递。

可以通过这个任务 ID 到 `/tasks/:id` 查询完整状态。

## 当前能力边界

当前审查能力是 **diff + changed-file-context reviewer**：

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

- Redis 分布式锁和限流
- 审计日志
- Tool Calling 框架
- tree-sitter 上下文裁剪
- 评测集和误报率统计

开发节奏见 [docs/roadmap.md](docs/roadmap.md)。
