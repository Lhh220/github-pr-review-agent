# GitHub PR 智能代码审查 Agent 设计文档

## 1. 项目目标

做一个基于 Go 的 GitHub PR 自动审查 Agent。接收 GitHub PR 事件后，异步执行代码审查，通过 Tool Calling 读取 diff、文件上下文、历史提交，生成结构化审查意见并回写 PR 评论。

定位：个人项目，面向后端开发 / Agent 开发岗位面试，重点体现 Agent 工程化和 Go 后端能力，不是套壳 LLM。

## 2. 核心价值

- 后端：Webhook 接入、异步任务队列、并发控制、分布式锁、状态机、审计、限流。
- Agent：Tool Calling 框架、上下文裁剪、结构化输出、评测集、token 成本控制。
- 真实可跑：能部署成 GitHub App，在自己仓库上演示。

## 3. 整体架构

当前实现已接入 RabbitMQ 延迟重试、死信队列、Worker Pool、Redis PR 级分布式锁、外部 API 限流、阶段三 Day 1 的 Tool Calling 基础框架、Day 2 的 5 个基础 GitHub 工具、Day 3 的 tree-sitter 函数级上下文裁剪、Day 4 的 `search_references` 跨文件引用检索，以及 Day 5 默认关闭的 `run_static_checks` 受限静态检查工具。默认链路仍是 `AGENT_MODE=legacy` 固定上下文审查；设置 `AGENT_MODE=tool_calling` 后启用 Agent 链路。

```text
GitHub PR Event
   |
   v
Webhook Receiver (Gin)
   |  签名校验 / action 过滤 / delivery_id 幂等
   v
RabbitMQ pr.review.queue
   |
   v
Worker Pool (Go)
   |  Redis 分布式锁（按 PR 维度）
   v
MySQL 原子 claim
   |
   v
GitHub API: PR meta + files + file context
   |  Redis 固定窗口限流
   v
DeepSeek LLM (结构化 JSON 输出)
   |  Redis 固定窗口限流
   v
MySQL review_task + review_result
   |
   v
GitHub PR Review API
```

下图是 `AGENT_MODE=tool_calling` 的当前 Agent 架构；`run_static_checks` 只有在 `AGENT_ENABLE_STATIC_CHECKS=true` 时才会注册：

```text
GitHub PR Event
   |
   v
Webhook Receiver (Gin)
   |  去重 / 签名校验
   v
RabbitMQ Queue
   |
   v
Worker Pool (Go)
   |  Redis 分布式锁（按 PR 维度）
   v
Agent Loop
   |-- Tool: get_pr_meta
   |-- Tool: list_changed_files
   |-- Tool: read_diff
   |-- Tool: read_file_context
   |-- Tool: search_references
   |-- Tool: get_commit_history
   |-- Tool: run_static_checks (optional)
   |  read_file_context -> tree-sitter / line fallback
   v
   |
   v
LLM Provider (Tool Calling, 结构化输出)
   |
   v
Review Formatter (bug/performance/style/security)
   |
   v
GitHub Comment Writer
   |
   v
MySQL (任务/结果/审计) + Redis (锁 / API 限流)
```

## 4. 模块设计

### 4.1 Webhook Receiver

- 路由：`POST /webhook/github`
- 校验 GitHub 签名（X-Hub-Signature-256）。
- 校验 `X-GitHub-Event` 必须是 `pull_request`，拒绝无关事件进入任务链路。
- 解析 PR 事件，只处理 `opened / synchronize / reopened`。
- 生成 taskId，幂等写入 MySQL（delivery_id 唯一），避免重复投递。
- 先把任务标记为 `queued`，再发布 `task_id` 到 RabbitMQ 并等待 publisher confirm，避免 Worker 在任务仍为 `received` 时提前消费并 ack；发布失败则标记为 `failed`。
- 服务收到退出信号后先停止 HTTP Server，再停止 Worker 并等待正在执行的任务，避免任务被硬中断。
- Webhook 入口当前依赖签名校验、事件过滤和 MySQL `delivery_id` 幂等；按仓库维度的 webhook 限流仍是后续增强。

### 4.2 任务队列

- 当前队列：`pr.review.queue`，durable queue。
- 延迟重试队列：`pr.review.retry.queue`，durable queue；消息设置 per-message TTL，过期后通过 DLX 回到主队列。
- 死信队列：`pr.review.dead_letter.queue`，durable queue，保存超过最大重试次数的任务消息。
- 消息体只放 `task_id`，具体数据从 MySQL 取，避免消息体过大。
- 重试消息额外携带 `attempt`，用于恢复数据库状态更新偶发失败时的执行次数。
- 消息使用 persistent delivery，Publisher 开启 confirm，确保 broker 已接收。
- Consumer 使用 manual ack；业务失败会发布到延迟重试队列后 ack 原消息，超过 `max_attempts` 后发布到死信队列并 ack。
- 连接或 channel 被服务端关闭后自动重建 RabbitMQ 连接，重连间隔按 2s 递增，最长 30s；进程退出仍然走优雅停机。
- 重试延迟按指数退避：默认 30s、60s，上限 10m；每次延迟附加随机 jitter，默认最多 5s，`REVIEW_RETRY_JITTER=0s` 可关闭。
- 主队列不携带 DLX 参数，避免线上已有队列因 queue argument 变化触发 PRECONDITION_FAILED。

### 4.3 Worker Pool

- 当前实现使用固定容量 slot 控制 Worker 并发，RabbitMQ prefetch 与 Worker 数一致。
- Worker 根据 `task_id` 从 MySQL 读取任务，原子 claim 到 `running`，调用 Review Service。
- 成功后更新为 `done`；可重试失败更新为 `retrying`；达到最大次数后更新为 `dead_letter`。
- `next_retry_at` 未到期的重复消息直接 ack，避免提前执行。
- 后台恢复循环每 30 秒扫描超过 6 分钟仍是 `running` 的任务，重新进入延迟重试链路，避免进程崩溃后任务卡死。
- 后台恢复循环同时扫描超过 60 秒仍是 `received` 或 `queued` 的任务，重新投递到主队列；这能覆盖“任务已落库但状态更新/发布前进程退出”的窗口，重复消息由原子 claim 和状态机兜底，不会重复审查。
- 如果 `retrying` 消息因时钟偏差等原因早于 `next_retry_at` 到达主队列，Worker 会按剩余等待时间重新写入延迟队列，而不是直接 Ack 丢掉队列载体。
- Worker 在原子 claim 之前先获取 Redis 分布式锁 `lock:review:pr:{repo}:{number}`，避免同一个 PR 的不同 task 被并发审查。
- 锁使用 `SET NX EX` 和随机 token；释放时通过 Lua 比较 token 后删除，避免误删其他 Worker 的锁。
- 锁默认 TTL 7 分钟，大于单次 review 超时 5 分钟；进程崩溃后锁自动过期，配合 running 超时恢复避免死锁。
- 如果锁被其他 Worker 持有，当前消息会以原 attempt 投递到延迟重试队列并 ack，不更新状态、不消耗业务重试次数。
- Redis 异常时消息 nack 回主队列，任务保持 `queued`，由队列重投和 queued 兜底恢复继续处理。

#### Worker 并发与限流

- PR 级互斥：`review:pr:{repo}:{number}`，大小写归一化，保证同一个 PR 同时只有一个 review。
- GitHub API 限流：`ratelimit:github:api`，默认 120 次 / 分钟，覆盖所有 GitHub REST 调用。
- DeepSeek 限流：`ratelimit:llm:deepseek`，默认 6 次 / 分钟，控制模型成本和上游压力。
- 限流使用 Redis Lua 脚本实现固定窗口计数；超限时 Worker 在当前任务 context 内等待窗口释放，避免直接把上游限流当作业务失败。
- 限流键跨进程共享，后续部署多副本时仍然生效。

### 4.4 Agent Loop

- Day 1 已实现基础 Agent Loop：构造 system/user 消息、发送工具定义、接收模型 `tool_calls`、调度注册表执行工具、把 tool result 回传模型、聚合 usage，并在工具预算耗尽后强制模型输出最终 JSON。
- 每轮工具调用有独立超时；未知工具、参数错误和工具执行错误会作为 tool result 回传给模型，避免一次工具选择错误直接导致任务失败。
- Day 2 已接入 5 个只读 GitHub 工具：`get_pr_meta`、`list_changed_files`、`read_diff`、`read_file_context`、`get_commit_history`。
- Day 4 已接入第 6 个只读工具 `search_references`，用于检索被删除、改名或影响面不明确的符号引用。
- Day 5 已接入第 7 个可选工具 `run_static_checks`，默认不注册；开启后把服务端白名单内的 `go test` / `go vet` 结果回传 Agent。
- Agent 执行多步推理：
  1. 调 `get_pr_meta` 了解 PR 标题、描述、改动文件列表。
  2. 调 `read_diff` 读取完整 diff。
  3. 对关注文件调 `read_file_context`，取函数、方法或类级上下文。
  4. 删除、改名或修改符号、字段、类型和配置键时，调 `search_references` 检查剩余引用。
  5. 必要时调 `get_commit_history` 理解修改动机。
  6. 工具可用且改动可能影响编译、测试或静态分析时，调 `run_static_checks` 获取确定性证据。
  7. 输出结构化审查意见。
- 每一步工具调用记录到 `tool_call_log`。

### 4.5 工具注册

统一 Tool 接口：
```go
type Tool interface {
    Name() string
    Description() string
    Schema() map[string]any
    Execute(ctx context.Context, input map[string]any) (string, error)
}
```
工具注册表 `Registry` 负责校验工具名、描述和 JSON Schema，避免重复注册，并输出稳定的工具定义列表。Agent 根据模型返回的 tool_call 调度。DeepSeek Provider 使用 OpenAI 兼容的 tools / tool_calls / tool 消息格式。

Day 2 的 GitHub Toolkit 绑定当前任务的 owner / repo / PR number，模型不能指定其他仓库或 PR。PR meta 和 changed files 在同一次任务内缓存，避免模型多轮工具调用时重复请求 GitHub。所有工具输出都是 JSON，并受 diff 行数、文件行范围、commit 数量、引用结果数量和输出字符数限制。`CreatePullRequestReview` 不注册为工具；写评论仍由服务端在 Agent 输出结构化结果后统一执行。前 6 个工具是只读工具；`run_static_checks` 是显式开启后才注册的受限执行工具。

### 4.6 上下文裁剪

`read_file_context` 当前的裁剪流程如下：

1. 从当前 PR 的 unified diff 中解析指定文件的新增行号；模型只传 `path` 时也能自动定位变更区域。
2. 用 tree-sitter 解析文件 AST，根据目标行找到所在或最相关的函数、方法、类或类型声明；Go / Python / JavaScript 已支持。命中嵌套符号时优先保留内层函数或方法，最多返回 8 个符号。
3. 不支持的语言、解析失败或找不到符号时，回退到围绕目标行的 bounded line range。

工具输出包含 `context_mode`（`tree_sitter` / `line_fallback`）、`language`、`symbols`、带行号的 `content`、截断标记和最终行范围。所有片段继续受 `MAX_FILE_CONTEXT_LINES` 与工具输出字符上限约束，避免超长上下文抬高 token 成本。

### 4.7 跨文件引用检索

`search_references` 的输入是一个精确标识符，例如 `MaxDiffLines`、`ValidateToken`，可选传入仓库相对路径前缀和最大结果数。它的执行流程如下：

1. 获取当前 PR head SHA，下载对应 commit 的 gzip tarball，保证扫描的是 PR 最新代码，而不是默认分支。
2. 流式遍历 tar 包，只扫描常见源码、配置和文档文件，跳过 `vendor/`、`node_modules/` 和 `.git/`。
3. 对每一行做标识符边界匹配，避免把 `MaxDiffLinesExtra` 误判成 `MaxDiffLines`。
4. 返回引用路径、行号、上下文片段，并标记该文件是否属于本次 PR 的变更文件。
5. 工具层限制最多 2000 个文件、128MB 解压后内容、单文件 2MB 和默认 100 条结果，最终输出继续受工具输出字符上限保护。

选择 tarball 而不是 GitHub Code Search，是因为 Code Search 主要面向默认分支索引，不能稳定检索 PR head；逐个读取 Git tree 文件则会消耗大量 GitHub API 配额。tarball 是一次下载、可流式处理、可严格限额的方案。

### 4.8 静态检查与能力边界

`run_static_checks` 的执行流程：

1. 获取 PR head SHA，下载对应 commit 的仓库 tarball。
2. 解压到 `AGENT_STATIC_CHECK_WORK_DIR` 下的临时目录；限制最多 2000 个文件、128MB 解压内容、单文件 2MB，拒绝路径穿越和 symlink / hardlink。
3. 根仓库没有 `go.mod` 时返回 `supported=false`，不执行命令。
4. 模型只能选择 `go_test` 或 `go_vet`；实际命令固定为 `go test ./...` / `go vet ./...`，不能传任意 shell。
5. 子进程使用独立的最小 Go 环境，只包含固定 `PATH`、`HOME`、Go cache / module cache / tmp 目录、`GOTOOLCHAIN=local`、`GOPROXY` 等白名单变量，不继承 GitHub App 私钥、LLM Key 或数据库密码。
6. 每条静态检查命令都有独立超时，整个工具调用继续受 Agent Tool Timeout 限制；单个命令输出最多 16KB，工具总输出继续受 40KB JSON 上限约束；临时仓库执行完删除。
7. `GOPROXY` 默认 `off`，禁止静态检查阶段下载新依赖；显式配置代理时表示接受该网络访问。

它目前是“受限本地静态检查模式”，不是强隔离沙箱：测试代码会被真实执行，也可能访问网络。个人项目和可信仓库演示可用；公开多租户场景应升级为独立容器、专用 runner 或 Firecracker / gVisor，并进一步禁网、限制 CPU / 内存。

当前能力：

- `AGENT_MODE=tool_calling` 能按需读取 PR 信息、diff、函数上下文、提交历史和跨文件引用。
- 开启 `AGENT_ENABLE_STATIC_CHECKS=true` 后，能把 `go test` / `go vet` 的确定性失败信息交给模型。
- 静态检查结果同样记录在 `tool_call_log`，可查询输入、输出、状态和耗时。
- 每条 finding 都要求携带工具返回的 `evidence`；确定性问题标记 `confirmed`，推测性问题标记 `needs_verification`。

后续增强：

- 扩充真实 MR 中出现过的误报和漏报样本。

目标示例：

```text
internal/config/config.go 删除了 MaxDiffLines 字段，
但 cmd/server/main.go:40 仍在引用 cfg.MaxDiffLines，
go test ./... 输出编译失败。
```

### 4.9 审查结果

结构化输出：
```json
{
  "summary": "整体评价",
  "findings": [
    {
      "category": "bug|performance|style|security",
      "file": "path",
      "line": 12,
      "severity": "high|medium|low",
      "comment": "具体问题",
      "suggestion": "修改建议",
      "confidence": "confirmed|needs_verification",
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
  ]
}
```

当前实现：

- DeepSeek 被要求只返回上述 JSON。
- `review.Service` 解析 JSON，过滤没有文件、行号或 evidence 的 finding；引用证据必须匹配同一文件的指定行和整行内容，静态检查证据必须来自对应命令输出。非法 confidence 会归一化为 `needs_verification`。
- 无 diff 和纯文档 PR 直接返回空 findings，不调用 LLM。
- 解析结果先写入 `review_result`，再回写 PR Review。
- `payload_json` 保存 findings，`raw_response` 保存模型原文。
- `model / input_tokens / output_tokens / total_tokens / llm_duration_ms` 同时落库。
- 如果模型偶发不按 JSON 返回，则降级为：summary 使用原文、findings 为空，避免整条任务失败。
- 回写策略当前是一条总结评论，包含结构化 findings；行内评论放到后续增强。

查询接口：`GET /tasks/:id/result`，返回任务状态和完整结构化结果。

### 4.10 存储设计

MySQL 表：
- `review_task`：id, repo, pr_number, commit_sha, status, attempt_count, max_attempts, next_retry_at, created_at, updated_at, error。
- `review_result`：id, task_id, summary, payload_json, raw_response, model, input_tokens, output_tokens, total_tokens, llm_duration_ms, created_at；`task_id` 唯一并外键关联 `review_task(id)`。
- `schema_migrations`：version, name, applied_at，记录已执行的数据库 migration。
- `tool_call_log`：id, task_id, tool_name, input_json, output_json, status, error, duration_ms, created_at；`task_id` 外键关联 `review_task(id)`。
- `audit_log`：id, task_id, action, old_status, new_status, detail_json, created_at；`task_id` 外键关联 `review_task(id)`。

数据库结构通过 `internal/store/migrations/*.up.sql` 管理，服务启动时自动执行；也可以通过 `go run ./cmd/migrate status` 和 `go run ./cmd/migrate up` 手动查看和执行。

当前 migration：

- `0001_init.up.sql`：`review_task`、`review_result`、基础索引和外键。
- `0002_audit_log.up.sql`：`audit_log`、任务索引、action 索引和外键。
- `0003_tool_call_log.up.sql`：`tool_call_log`、任务索引、工具名索引和外键。

死信管理接口：
- `GET /dead-letters`：按仓库、PR、limit 查询死信任务。
- `POST /dead-letters/:id/requeue`：把死信任务改回 `queued`，重置 `attempt_count`，并重新投递主队列。

审计与观测接口：
- `GET /audit-logs`：按 task_id、repo、PR、action、limit 查询任务审计轨迹。
- `GET /stats`：按 repo 聚合任务状态、成功率、重试事件、耗时、findings 和 token 用量。
- `GET /tasks/:id/tool-calls`：按任务查询 Agent 工具调用轨迹、输入输出和耗时。

审计 action：
- `task_created`：Webhook 首次创建任务。
- `task_status_changed`：任务状态流转，detail 记录 attempt、错误信息、下次重试时间等。
- `review_result_created`：结构化审查结果落库，detail 记录模型、finding 数量、token 用量和 LLM 耗时。

Redis Key：
- `lock:review:pr:{repo}:{number}`：PR 级分布式锁。
- `ratelimit:github:api`：GitHub API 固定窗口限流。
- `ratelimit:llm:deepseek`：DeepSeek 固定窗口限流。
- 事件去重当前由 MySQL `delivery_id` 唯一约束实现，不依赖 Redis。

### 4.11 状态机

```text
received -> queued -> running -> done
                        |
                        +--> retrying -> running -> ...
                        |
                        +--> dead_letter

running -> failed
```

`failed` 保留给队列发布失败等不可重试的基础设施错误；业务审查失败优先走 `retrying`，达到最大次数后进入 `dead_letter`。

### 4.12 开发者后台

`internal/adminui` 提供轻量 Admin Console，静态 HTML / CSS / JS 通过 `go:embed` 打进服务二进制，路由为 `/admin`。

后台复用现有管理 API，不直接访问 MySQL：

```text
/stats
/tasks
/tasks/:id/result
/tasks/:id/tool-calls
/audit-logs
/dead-letters
/dead-letters/:id/requeue
```

页面在浏览器中保存 `ADMIN_TOKEN` 到当前标签页的 `sessionStorage`，每次请求带 Bearer Token。后台本身不保存 token，也不渲染任何秘钥。

当前视图：

- Overview：核心指标、状态分布和最近任务。
- Tasks：任务筛选和详情。
- Task Detail：任务信息、结构化审查结果、raw output、工具调用轨迹和审计轨迹。
- Dead Letters：死信列表和 Requeue 操作。
- Audit：按仓库、PR、action 和 limit 筛选审计日志。

该后台定位是开发调试和项目演示，不是完整 APM；Prometheus / Grafana / 日志平台放后续生产化阶段。

## 5. 技术选型

| 组件 | 选型 | 理由 |
---|---|---|
| 语言 | Go | 简历主语言，并发模型适合 Worker |
| HTTP | Gin | 已有栈，中间件鉴权/限流方便 |
| 队列 | RabbitMQ | 面试常问，支持死信、重试 |
| 缓存/锁 | Redis | 分布式锁、去重、限流 |
| 数据库 | MySQL + database/sql + embedded SQL migration | 任务/结果/审计持久化，依赖少且部署简单 |
| 代码解析 | tree-sitter | 多语言 AST，函数级上下文裁剪 |
| LLM | DeepSeek-V3 默认 / OpenAI gpt-4o-mini 备选 | 便宜、代码强；需要稳定 Tool Calling 时切 OpenAI |
| 部署 | Docker Compose | 本地依赖一键起 |

LLM Provider 抽象：
```go
type Provider interface {
    ChatWithTools(ctx context.Context, req ChatRequest) (ChatResponse, error)
}
```
先实现 DeepSeek Provider（OpenAI 兼容），后续可替换为 OpenAI / Anthropic。

## 6. 关键流程

一个 PR 从接收到回写评论：

1. GitHub 发 webhook 到 `/webhook/github`。
2. Receiver 校验签名、解析事件、去重，在同一个事务里写 `review_task(received)` 和 `audit_log(task_created)`。
3. Webhook 先把任务标记为 `queued`，发布 `task_id` 到 RabbitMQ 并等待 publisher confirm，然后返回 202。
4. Worker 消费消息，先获取 PR 级 Redis 锁，再原子 claim 并把状态置 `running`。
5. Review Service 在 Redis 限流下读取 PR meta、diff 和变更文件上下文。
6. DeepSeek 在 Redis 限流下生成结构化 JSON，在同一个事务里写 `review_result` 和 `audit_log(review_result_created)`。
7. Review Service 回写 GitHub PR Review，随后释放 PR 锁。
8. Worker 将任务标记为 `done` 并 ack 消息，同时写状态审计；可重试失败进入 `retrying`，达到最大次数后进入 `dead_letter`。

`AGENT_MODE=tool_calling` 时，第 5 步会扩展为多步工具调用；开启静态检查后，Agent 可把工具输出作为代码级验证证据。

## 7. 安全与成本

- Webhook 签名校验，防止伪造。
- `APP_ENV=production` 时启动强制要求 `GITHUB_WEBHOOK_SECRET` 和 `ADMIN_TOKEN`，避免生产环境误配置。
- 任务查询接口使用 Bearer Token 鉴权。
- Webhook 通过 `delivery_id` 唯一约束做幂等，GitHub 重发事件不会重复执行审查。
- GitHub 工具只读；`run_static_checks` 默认关闭，开启后只执行服务端白名单命令，且不继承敏感环境变量。
- Redis 限流保护 GitHub API 和 DeepSeek API，避免异常流量放大到上游。
- token 成本统计：每次 LLM 调用记录 input/output tokens，落库。
- 超时控制：每个任务最大执行时间，防止卡死。
- Redis 锁 TTL 大于任务超时，Worker 崩溃后锁自动释放，避免永久阻塞同一个 PR。

## 8. 评测

Day 7 实现了离线优先的评测框架。每个样本包含三部分：

- `fixture/case.json`：模拟 PR 标题、描述、head SHA、diff、变更文件内容和仓库快照。
- `fixture/script.json`：离线模式下的 provider 响应脚本，包括工具调用请求、token 用量和模型耗时。
- `expected.json`：人工标注的问题类别、文件、行号、严重级别和期望 confidence。

离线模式会把这些 fixture 接到真实的 `AgentService`、Agent Loop 和 GitHub 工具上：GitHub API 被 fixture 替代，工具真实执行，模型响应由脚本替代。因此它可以验证工具输入、工具输出、evidence 校验、confidence 降级、docs-only 跳过和指标统计，同时保持确定性和零 API 成本。

匹配规则按文件、行号邻近度（默认 3 行内）和类别做贪心匹配；类别错误记为误报，未被正确类别命中的标注记为漏报。`category_accuracy` 记录可匹配位置的预测中类别正确的比例。`confirmed_precision` 的分子还要求人工标注的 confidence 为 confirmed；把 needs_verification 报成 confirmed 会记入 `overconfirmed_findings`。这仍是位置与类别匹配的近似指标，无法证明 comment 的语义正确；live 验收必须人工检查报告中的完整 `case_results[].findings`。

当前指标：

- `precision`：报出问题中命中人工标注的比例。
- `recall`：人工标注问题中被命中的比例。
- `false_positive_rate`：实际执行审查的无问题样本中出现任意乱报的比例；排除 docs-only 跳过与运行失败，分母见 `negative_cases`。
- `category_accuracy`：可匹配位置的预测中类别正确的比例。
- `confirmed_precision`：confirmed finding 中命中位置、类别且标注也为 confirmed 的比例。
- `failed_cases / scored_cases / planned_cases`：运行失败数、成功执行数、计划执行数；失败不当作漏报或正确的空结果。
- `overconfirmed_findings`：位置与类别命中，但把 needs_verification 标注过度确认为 confirmed 的数量。
- `avg_input_tokens / avg_output_tokens / avg_total_tokens`：平均 token 成本。
- `avg_latency_ms`：平均耗时。
- `case_results[].tools`：每个 case 的工具调用轨迹。

运行方式：

```powershell
go run ./cmd/eval
$env:DEEPSEEK_API_KEY="..."
go run ./cmd/eval -live -runs 3 -timeout 30m -report eval/report-live.json
```

离线报告输出到 `eval/report.json`，适合提交为回归基线；live 模式复用 fixture GitHub 客户端，只调用真实模型，不回写 GitHub 评论。live 支持 `-runs` 重复执行并记录轮次，用于观察模型输出波动。评测集随项目迭代，优先补充真实 MR 中出现过的误报和漏报样本。

每个 case 完成或失败后立即保存报告；单个错误记录在 `case_results[].error` 后继续后续样本，总超时后停止。存在失败或未完成样本时 CLI 返回非零状态。`mode` 区分 offline/live，live 报告记录配置的 `model`。平均 token 与延迟仅统计成功执行，失败调用的费用不包含在内。7 个样本中有 2 个正常代码负样本（已初始化 map、参数化 SQL），会经过模型和工具链；文档样本只验证跳过逻辑。

## 9. 后续可选扩展

- 支持 GitLab / Gitea。
- 支持增量审查：只审查相对上次审查的新 commit。
- 支持 MCP 协议工具层，和金山实习的 MCP 工具形成对比。
- 接入向量库，检索相似历史 PR。
