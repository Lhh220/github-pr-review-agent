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
- 提供 `/audit-logs` 查询任务审计轨迹，`/stats` 查询任务成功率、重试次数、token 用量和平均耗时
- 提供轻量开发者后台 `/admin`，可视化任务、审查结果、Agent 工具调用轨迹、审计日志和死信管理
- 阶段三 Day 1 已完成 Agent 基础框架：Tool 接口、工具注册表、Agent Loop、DeepSeek tool calls、`tool_call_log` 和 `/tasks/:id/tool-calls`
- 阶段三 Day 2 已接入真实 GitHub 工具：`get_pr_meta`、`list_changed_files`、`read_diff`、`read_file_context`、`get_commit_history`
- 阶段三 Day 3 已接入 tree-sitter：`read_file_context` 支持 Go / Python / JavaScript 函数级上下文，其他文件回退到有界行范围
- 阶段三 Day 4 已接入 `search_references`：下载 PR head 的仓库 tarball，流式扫描跨文件精确标识符引用
- 阶段三 Day 5 已接入 `run_static_checks`：默认关闭，开启后可在服务端白名单内执行 `go test` / `go vet` 并把结果回传 Agent
- 阶段三 Day 6 已完成结构化输出增强：每条 finding 携带 `evidence`，并按 `confirmed / needs_verification` 标注可信度
- 阶段三评测集已扩展为 9 个离线 PR fixture，包含 4 个正常代码负样本，统计 precision / recall / 误报率 / 分类准确率 / confidence 校准 / token / 延迟 / 工具轨迹
- 关键状态变更与审查结果创建会同步写入 `audit_log`，任务数据和审计数据保持同一事务
- MySQL 结构通过版本化 migration 管理，服务启动自动执行，也提供 `cmd/migrate` CLI

Day 5 的审计表和观测统计已完成本地与线上验收，阶段二收官。阶段三 Day 1 的 Agent 框架、Day 2 的 5 个基础 GitHub 工具、Day 3 的 tree-sitter 上下文裁剪、Day 4 的跨文件引用检索、Day 5 的受限静态检查工具、Day 6 的 evidence / confidence 结构化输出、Day 7 的基础评测集已完成本地验收；Day 7 已部署并通过 PR #23 线上冒烟，`/healthz` 返回正常。线上默认仍是 `AGENT_MODE=legacy`，把 Railway 变量改成 `AGENT_MODE=tool_calling` 后即可启用 Agent 审查链路。

当前线上示例：

```text
https://github-pr-review-agent-production.up.railway.app
```

健康检查：

```text
GET /healthz
```

开发者后台：

```text
https://github-pr-review-agent-production.up.railway.app/admin
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
AGENT_MODE=legacy
AGENT_MAX_STEPS=8
AGENT_TOOL_TIMEOUT=20s
AGENT_MAX_COMMIT_HISTORY=20
AGENT_MAX_REFERENCE_RESULTS=100
AGENT_ENABLE_STATIC_CHECKS=false
AGENT_STATIC_CHECK_TIMEOUT=2m
AGENT_STATIC_CHECK_WORK_DIR=.static-checks
AGENT_STATIC_CHECK_GOPROXY=off
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
- `AGENT_MODE`：`legacy` 表示固定上下文审查链路；`tool_calling` 表示启用 GitHub Tool Calling Agent 链路。默认 `legacy`，便于回滚。
- `AGENT_MAX_STEPS`：Agent 最大工具调用轮次，默认 8。
- `AGENT_TOOL_TIMEOUT`：单个工具执行超时，默认 20s。
- `AGENT_MAX_COMMIT_HISTORY`：`get_commit_history` 最多返回多少个 commit，默认 20，工具内部最大会限制到 100。
- `AGENT_MAX_REFERENCE_RESULTS`：`search_references` 最多返回多少条引用，默认 100；工具内部还会限制扫描文件数和解压后字节数。
- `AGENT_ENABLE_STATIC_CHECKS`：是否向 Agent 注册 `run_static_checks`，默认 `false`。只有同时设置 `AGENT_MODE=tool_calling` 才会生效。
- `AGENT_STATIC_CHECK_TIMEOUT`：`go_test` / `go_vet` 每个命令的独立超时，默认 `2m`；整个工具调用还受 `AGENT_TOOL_TIMEOUT` 限制。
- `AGENT_STATIC_CHECK_WORK_DIR`：静态检查工作目录。Docker 默认使用 `/workspace/.static-checks`，本地建议显式配置到 D 盘项目目录下。
- `AGENT_STATIC_CHECK_GOPROXY`：静态检查下载依赖使用的 Go proxy，默认 `off` 表示禁止下载新依赖。线上需要首次下载依赖时可配置为 `https://goproxy.cn,direct`，这表示明确允许该网络访问。

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

   当前包含四个 migration：version 1 `init`、version 2 `audit_log`、version 3 `tool_call_log`、version 4 `review_delivery`。首次部署新数据库时，启动日志会依次出现应用记录；已执行过则显示 up to date。

   如果本地数据库已经执行过，则只会看到 `mysql migrations are up to date`。

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

tree-sitter 官方 Go binding 依赖 CGO。Linux/macOS 通常可直接运行；Windows 本地如默认禁用 CGO，需要先启用并指定可用的 C 编译器：

```powershell
$env:CGO_ENABLED="1"
$env:CC="<gcc 路径>"
```

```powershell
go test ./...
go vet ./...
```

Railway / Docker 生产构建使用仓库根目录的 `Dockerfile`。构建阶段会安装 `gcc` 和 `musl-dev`，并强制 `CGO_ENABLED=1`；运行阶段使用同 Alpine 基础镜像，并带 Go 工具链和编译器，保证 CGO 二进制运行一致，也让 `run_static_checks` 开启后可以执行 `go test` / `go vet`。

## 评测

评测集位于 `eval/cases`，当前包含 9 个离线可回归样本：

- `001-delete-field`：删除配置字段后仍被跨文件引用，期望 `bug / confirmed`
- `002-nil-map`：写入 nil map，期望 `bug / confirmed`
- `003-sql-concat`：拼接 SQL，期望 `security / confirmed`
- `004-goroutine-leak`：无退出条件的后台 goroutine，期望 `performance / needs_verification`
- `005-docs-only`：纯文档 PR，期望 0 findings 且不调用工具
- `006-initialized-map`：先初始化 map 再写入，期望 0 findings，经过模型和工具审查
- `007-parameterized-sql`：SQL 使用参数绑定，期望 0 findings，经过模型和工具审查
- `008-diff-section-contract`：生成器与解析器使用兼容的文件标题格式，期望 0 findings
- `009-review-output-policy`：明确的 JSON 校验和 performance 降级约定，与测试和评测逻辑一致，期望 0 findings

离线模式使用 fixture script 驱动真实 Agent Loop 和 GitHub 工具，不访问外网、不消耗模型 token，适合作为回归测试：

```powershell
go run ./cmd/eval
```

报告输出到 `eval/report.json`，包含 `precision`、`recall`、`false_positive_rate`、`category_accuracy`、`confirmed_precision`、平均 token、平均耗时和每个 case 的工具调用轨迹。

如需测试真实 DeepSeek 输出，使用同一批 fixture 和真实工具链，但不会向 GitHub 发评论：

```powershell
$env:DEEPSEEK_API_KEY="你的 key"
go run ./cmd/eval -live -runs 3 -timeout 30m -report eval/report-live.json
```

`-runs` 会让每个样本重复执行，并在 `case_results[].run` 中记录轮次。live 模型输出存在波动，建议至少跑 3 轮后再解读 precision / recall 和 confirmed precision；报告里的 `cases` 是总执行次数。

每个样本完成或失败后都会保存报告；个别失败会记录 `error` 并继续，总超时则保留已有结果后退出。失败或未完成时退出码非零。验收先检查 `failed_cases=0` 且 `cases=planned_cases`，再看质量指标和完整 `case_results[].findings`。误报率排除直接跳过的文档样本，`confirmed_precision` 要求命中类别且人工标注也为 confirmed。位置、类别匹配仍不能代替人工核对结论语义。报告的 `mode` 区分离线脚本回归与真实模型评测。

审查链路遇到非法 JSON、空 summary 或缺失/null findings 数组会返回错误并进入任务重试；不会把格式错误发布为“未发现问题”。静态检查证据要求非空摘录，命令已结束、退出码大于零且没有超时或启动错误。引用文本的真实性校验不等于缺陷语义证明，模型结论仍需通过 live 评测确认质量。

交付验收状态、剩余线上操作、简历表述和面试问答见 [docs/delivery.md](docs/delivery.md)。

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
- Worker 每 30 秒扫描一次超过 60 秒仍是 `received` 或 `queued` 的任务，自动重新投递；重复消息由原子 claim 和状态机兜底。早到的 `retrying` 消息会按剩余延迟重新入队，避免被过早 Ack 后卡住。

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
        "confidence": "confirmed",
        "evidence": [
          {
            "type": "reference",
            "file": "cmd/server/main.go",
            "line": 42,
            "text": "cfg.MaxDiffLines"
          },
          {
            "type": "static_check",
            "command": "go test ./...",
            "excerpt": "undefined: cfg.MaxDiffLines"
          }
        ]
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

- `findings` 是结构化问题列表，字段包括 `category / file / line / severity / comment / suggestion / confidence / evidence`。
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

查询任务审计日志：

```text
GET /audit-logs?task_id=1&repo=owner/repo&pr=12&action=task_status_changed&limit=20
Authorization: Bearer <ADMIN_TOKEN>
```

支持的可选参数：

- `task_id`：只看某个任务。
- `action`：`task_created`、`task_status_changed`、`review_result_created`。
- `limit`：默认 50，最大 200。

返回示例：

```json
{
  "audit_logs": [
    {
      "id": 12,
      "task_id": 1,
      "repo": "Lhh220/github-pr-review-agent",
      "pr_number": 7,
      "action": "task_status_changed",
      "old_status": "running",
      "new_status": "done",
      "detail": {},
      "created_at": "2026-09-05T12:00:00Z"
    }
  ]
}
```

查询观测统计：

```text
GET /stats?repo=Lhh220/github-pr-review-agent
Authorization: Bearer <ADMIN_TOKEN>
```

返回内容包括任务总数、各状态数量、成功率、重试事件数、平均任务耗时、审查结果数、findings 总数、token 用量和 LLM 平均耗时。当前实现直接从 MySQL 聚合，适合个人项目规模；数据量变大后再引入汇总表或 Prometheus。

查询任务工具调用轨迹：

```text
GET /tasks/<task_id>/tool-calls
Authorization: Bearer <ADMIN_TOKEN>
```

返回工具名、输入、输出、状态、错误和耗时。`AGENT_MODE=tool_calling` 的任务会记录每一步工具调用；`legacy` 任务不会产生工具调用记录。

### 轻量开发者后台

访问：

```text
GET /admin
```

后台页面内嵌在 Go 服务二进制中，不需要单独部署前端。页面复用现有管理 API，并在浏览器中携带 `Authorization: Bearer <ADMIN_TOKEN>`。`ADMIN_TOKEN` 只保存在当前浏览器标签页的 `sessionStorage` 中，关闭标签页后清除。

当前提供：

- Overview：任务总数、成功率、状态分布、重试事件、token 用量、平均任务耗时和平均 LLM 耗时。
- Tasks：按仓库、状态、PR 和 limit 筛选任务。
- Task Detail：任务状态、审查 summary、findings、raw model output、Agent tool calls 输入输出和审计轨迹。
- Dead Letters：查看死信任务并执行 Requeue。
- Audit：按仓库、PR、action 和 limit 筛选任务状态流转和结果创建审计。
- Auto Refresh：每 10 秒刷新当前视图和已选中的任务详情。

Day 2 Agent 模式线上验收步骤：

1. push 代码到 `main`，等待 Railway 部署完成。
2. 在 Railway 中把 `AGENT_MODE` 改成 `tool_calling`，保留 `AGENT_MAX_STEPS=8`、`AGENT_TOOL_TIMEOUT=20s`、`AGENT_MAX_COMMIT_HISTORY=20`、`AGENT_MAX_REFERENCE_RESULTS=100`。
3. 提一个包含代码改动的测试 PR。
4. 日志应出现 `agent review mode enabled`，bot 评论后记录评论里的 Task ID。
5. 请求 `/tasks/<task_id>/tool-calls`，应能看到模型调用 GitHub 工具的输入、输出和耗时。
6. 验收完成后可保留 `tool_calling`；如果质量不稳定，把 `AGENT_MODE` 改回 `legacy` 即可回滚。

Day 5 线上验收步骤：

1. push 代码到 `main`，等待 Railway 部署完成。
2. 在 Railway 日志里确认 version 2 migration 已应用或已是 up to date。
3. 请求 `/healthz`、`/audit-logs?limit=5`、`/stats`，三者都应返回 200。
4. 提一个测试 PR，等 bot 评论后，用评论里的 Task ID 调 `/audit-logs?task_id=<id>`，应能看到 `task_created`、多次 `task_status_changed` 和 `review_result_created`。

阶段三 Day 5 静态检查线上验收步骤：

1. push 代码到 `main`，等待 Railway 部署完成。
2. 设置 `AGENT_MODE=tool_calling`、`AGENT_ENABLE_STATIC_CHECKS=true`、`AGENT_TOOL_TIMEOUT=3m`、`AGENT_STATIC_CHECK_TIMEOUT=2m`。
3. 如需下载依赖，设置 `AGENT_STATIC_CHECK_GOPROXY=https://goproxy.cn,direct`；如果保持 `off`，依赖必须在本地 Go module cache 中已存在。
4. 提一个会引入编译错误的 PR。
5. bot 评论应引用 `go test ./...` 或 `go vet ./...` 的失败输出；在 `/tasks/<task_id>/tool-calls` 中应能看到 `run_static_checks` 的输入、输出和耗时。

## 当前能力边界

### 评论回写与失败恢复

`review_delivery` 在分析前保存首次执行时的 PR head 和随机发布标识。分析结果先存入 `review_result`，重试时优先复用，避免重复模型调用和结果唯一键冲突。发布前分页查询该 PR 的 reviews，用标识、commit 和已提交状态核对；已存在则补记 GitHub Review ID，否则显式指定 `commit_id` 发布。Review ID、任务 done 和审计日志在同一个 MySQL 事务中提交。`/tasks/:id/result` 返回 `delivery.commit_sha / github_review_id`；历史结果没有 delivery 时为 null。

这是 PR 锁保护下的幂等恢复，不是跨 GitHub/MySQL 的严格 exactly-once。查询与发布仍非原子，远端响应不确定且列表尚未可见、锁失效或人工删除标识等边界不能被完全消除。查询失败或分页超限不会继续发布。已有结果但缺少发布记录的旧任务要求人工核对，不会自动补发；相同任务重试只补发原版本结果，不重新审查新版本。新 head 应由新的 Webhook 任务处理。首次处理前已过期的 Webhook 合并/跳过策略仍属后续版本治理。

新增 `.github/workflows/ci.yml`，推送或 PR 会运行 Go 测试、MySQL 集成测试、vet 和离线评测。工作流需推送后才能确认远端运行结果。

默认 `legacy` 审查能力是 **diff + changed-file-context reviewer**：

- 会把 PR diff 和变更文件内容交给 LLM
- 还看不到改动文件之外的关联代码
- 无法确认被删除的字段、函数、类型是否仍被其他文件引用
- 默认不执行编译、测试或静态检查；显式开启 `run_static_checks` 后，可以回传服务端白名单内的 `go test` / `go vet` 结果

代码库已经具备 Tool Calling 基础框架、6 个基础 GitHub 工具和工具调用日志；线上启用 `AGENT_MODE=tool_calling` 后，模型可以多轮读取 PR 信息、diff、指定文件上下文、提交历史和跨文件引用。`read_file_context` 会从 diff 推断变更行，并用 tree-sitter 提取 Go / Python / JavaScript 的函数、方法或类上下文；TypeScript、Java 等其他语言暂回退到有界行范围。`search_references` 会以 PR head 为准扫描仓库 tarball，用标识符边界匹配剩余引用，并返回文件、行号和上下文片段。开启 `AGENT_ENABLE_STATIC_CHECKS` 后，模型还可以调用第 7 个工具 `run_static_checks` 获取确定性检查结果。审查结果会输出 `confidence` 和 `evidence`；服务端会按文件、行号和整行内容校验引用证据，只有具备工具证据的确定性问题才标记为 `confirmed`，推测性问题标记为 `needs_verification`。

`run_static_checks` 是“受限本地静态检查模式”，不是强隔离沙箱：命令和参数由服务端固定为 `go test ./...` / `go vet ./...`，环境变量不包含 GitHub 和 LLM 密钥，解压限制文件数和大小并拒绝链接条目；但 PR 中的测试代码本身仍会被执行，也可能通过网络访问外部服务。个人仓库演示可用，公开多租户服务应改为独立容器或专用 runner，并禁网、限 CPU / 内存。

详细设计见 [docs/design.md](docs/design.md)。

## 后续计划

- 执行 live 评测和静态检查线上专项验收
- 扩充真实 MR 中的误报 / 漏报样本
- 公开多租户场景升级独立沙箱 runner

开发节奏见 [docs/roadmap.md](docs/roadmap.md)。
