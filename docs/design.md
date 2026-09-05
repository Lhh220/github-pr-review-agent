# GitHub PR 智能代码审查 Agent 设计文档

## 1. 项目目标

做一个基于 Go 的 GitHub PR 自动审查 Agent。接收 GitHub PR 事件后，异步执行代码审查，通过 Tool Calling 读取 diff、文件上下文、历史提交，生成结构化审查意见并回写 PR 评论。

定位：个人项目，面向后端开发 / Agent 开发岗位面试，重点体现 Agent 工程化和 Go 后端能力，不是套壳 LLM。

## 2. 核心价值

- 后端：Webhook 接入、异步任务队列、并发控制、分布式锁、状态机、审计、限流。
- Agent：Tool Calling 框架、上下文裁剪、结构化输出、评测集、token 成本控制。
- 真实可跑：能部署成 GitHub App，在自己仓库上演示。

## 3. 整体架构

当前实现已接入 RabbitMQ 延迟重试、死信队列、Worker Pool、Redis PR 级分布式锁和外部 API 限流；还没有接入 Tool Calling、tree-sitter 和静态检查沙箱。当前实际链路是：

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

下图是后续目标架构，Tool Calling 和静态检查沙箱还未实现：

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
   |-- Tool: run_static_checks
   |-- Tool: get_commit_history
   v
Context Builder (tree-sitter 裁剪)
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
- 后台恢复循环同时扫描超过 60 秒仍是 `queued` 的任务，重新投递到主队列；重复消息由原子 claim 和状态机兜底，不会重复审查。
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

- 输入：任务上下文（仓库、PR、commit）。
- Agent 执行多步推理：
  1. 调 `get_pr_meta` 了解 PR 标题、描述、改动文件列表。
  2. 调 `read_diff` 读取完整 diff。
  3. 对关注文件调 `read_file_context`，用 tree-sitter 取函数级上下文。
  4. 对被删除或改名的字段、函数、类型，调 `search_references` 确认是否仍有引用。
  5. 必要时调 `run_static_checks` 获取编译、测试或静态分析结果。
  6. 必要时调 `get_commit_history` 理解修改动机。
  7. 输出结构化审查意见。
- 每一步工具调用记录到审计表。

### 4.5 工具注册

统一 Tool 接口：
```go
type Tool interface {
    Name() string
    Schema() jsonschema.Schema
    Execute(ctx context.Context, input map[string]any) (string, error)
}
```
工具注册表 `ToolRegistry`，Agent 根据模型返回的 tool_call 调度。每个工具定义 JSON Schema，供 LLM function calling 使用。

### 4.6 上下文裁剪

- diff 可能很大，不能全塞给 LLM。
- 策略：
  - 按文件优先级排序：源码 > 配置 > 文档。
  - 每个文件只取变更点附近函数级上下文，用 tree-sitter 解析 AST。
  - 设置 token 预算，超出则降级为只看 diff。
- 目标：控制单次审查 token 成本，避免超长导致质量下降。

### 4.7 当前能力边界与增强方向

当前 MVP 是 **diff + changed-file-context reviewer**：

- PR 无变更文件时短路处理，直接回固定评论，不调用 LLM。
- 把 PR diff 交给 LLM。
- 额外读取部分变更文件的完整内容，并按行数裁剪后一起送给 LLM。
- 看不到改动文件之外的关联代码。
- 无法确认被删除的字段、函数、类型是否仍被其他文件引用。
- 无法验证 PR 是否能通过编译、测试或静态检查。

因此它只能给出“可能存在风险”的提示，不能把跨文件引用问题定位成确定的编译错误。

下一阶段要把它升级成 **code-aware agent**：

1. `read_file_context`
   - 读取变更文件及关联文件的完整上下文。
   - 优先读取被改动函数、结构体、接口的定义和使用位置。
2. `search_references`
   - 对被删除或改名的符号做引用检索。
   - 输出引用文件、行号和上下文片段。
3. `run_static_checks`
   - 在隔离环境中执行 `go test`、`go vet` 或编译检查。
   - 把失败信息回传给 Agent，作为确定性证据。
4. 结论分级
   - `confirmed`：有代码上下文或静态检查证据。
   - `needs_verification`：仅有 diff 推理，缺少跨文件证据。

目标示例：

```text
internal/config/config.go 删除了 MaxDiffLines 字段，
但 cmd/server/main.go:40 仍在引用 cfg.MaxDiffLines，
go test ./... 会编译失败。
```

静态检查安全要求：

- 在一次性容器或临时目录中执行。
- 不注入 GitHub App 私钥、LLM Key 等敏感环境变量。
- 限制 CPU、内存、执行时长和网络访问。
- 只允许执行白名单命令，避免把 PR 中的代码当成任意命令执行。

### 4.8 审查结果

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
      "confidence": "confirmed|needs_verification"
    }
  ]
}
```

当前实现：

- DeepSeek 被要求只返回上述 JSON。
- `review.Service` 解析 JSON，先写入 `review_result`，再回写 PR Review。
- `payload_json` 保存 findings，`raw_response` 保存模型原文。
- `model / input_tokens / output_tokens / total_tokens / llm_duration_ms` 同时落库。
- 如果模型偶发不按 JSON 返回，则降级为：summary 使用原文、findings 为空，避免整条任务失败。
- 回写策略当前是一条总结评论，包含结构化 findings；行内评论放到后续增强。

查询接口：`GET /tasks/:id/result`，返回任务状态和完整结构化结果。

### 4.9 存储设计

MySQL 表：
- `review_task`：id, repo, pr_number, commit_sha, status, attempt_count, max_attempts, next_retry_at, created_at, updated_at, error。
- `review_result`：id, task_id, summary, payload_json, raw_response, model, input_tokens, output_tokens, total_tokens, llm_duration_ms, created_at；`task_id` 唯一并外键关联 `review_task(id)`。
- `schema_migrations`：version, name, applied_at，记录已执行的数据库 migration。
- `tool_call_log`：id, task_id, tool_name, input_json, output_json, tokens, duration_ms, created_at。
- `audit_log`：id, task_id, action, old_status, new_status, detail_json, created_at；`task_id` 外键关联 `review_task(id)`。

数据库结构通过 `internal/store/migrations/*.up.sql` 管理，服务启动时自动执行；也可以通过 `go run ./cmd/migrate status` 和 `go run ./cmd/migrate up` 手动查看和执行。

当前 migration：

- `0001_init.up.sql`：`review_task`、`review_result`、基础索引和外键。
- `0002_audit_log.up.sql`：`audit_log`、任务索引、action 索引和外键。

死信管理接口：
- `GET /dead-letters`：按仓库、PR、limit 查询死信任务。
- `POST /dead-letters/:id/requeue`：把死信任务改回 `queued`，重置 `attempt_count`，并重新投递主队列。

审计与观测接口：
- `GET /audit-logs`：按 task_id、action、limit 查询任务审计轨迹。
- `GET /stats`：按 repo 聚合任务状态、成功率、重试事件、耗时、findings 和 token 用量。

审计 action：
- `task_created`：Webhook 首次创建任务。
- `task_status_changed`：任务状态流转，detail 记录 attempt、错误信息、下次重试时间等。
- `review_result_created`：结构化审查结果落库，detail 记录模型、finding 数量、token 用量和 LLM 耗时。

Redis Key：
- `lock:review:pr:{repo}:{number}`：PR 级分布式锁。
- `ratelimit:github:api`：GitHub API 固定窗口限流。
- `ratelimit:llm:deepseek`：DeepSeek 固定窗口限流。
- 事件去重当前由 MySQL `delivery_id` 唯一约束实现，不依赖 Redis。

### 4.10 状态机

```text
received -> queued -> running -> done
                        |
                        +--> retrying -> running -> ...
                        |
                        +--> dead_letter

running -> failed
```

`failed` 保留给队列发布失败等不可重试的基础设施错误；业务审查失败优先走 `retrying`，达到最大次数后进入 `dead_letter`。

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

后续引入 Tool Calling 和静态检查后，再扩展为多步工具调用、审计链路和代码级验证。

## 7. 安全与成本

- Webhook 签名校验，防止伪造。
- `APP_ENV=production` 时启动强制要求 `GITHUB_WEBHOOK_SECRET` 和 `ADMIN_TOKEN`，避免生产环境误配置。
- 任务查询接口使用 Bearer Token 鉴权。
- Webhook 通过 `delivery_id` 唯一约束做幂等，GitHub 重发事件不会重复执行审查。
- 工具调用只读，不执行写操作（除回写评论）。
- Redis 限流保护 GitHub API 和 DeepSeek API，避免异常流量放大到上游。
- token 成本统计：每次 LLM 调用记录 input/output tokens，落库。
- 超时控制：每个任务最大执行时间，防止卡死。
- Redis 锁 TTL 大于任务超时，Worker 崩溃后锁自动释放，避免永久阻塞同一个 PR。

## 8. 评测

- 构造一批 PR 样本，人工标注应有问题。
- 指标：
  - 准确率：提出的审查意见中正确比例。
  - 误报率：无问题却报问题的比例。
  - 覆盖率：人工标注问题中被命中的比例。
- 评测集随项目迭代，持续优化提示词和工具链。

## 9. 后续可选扩展

- 支持 GitLab / Gitea。
- 支持增量审查：只审查相对上次审查的新 commit。
- 支持 MCP 协议工具层，和金山实习的 MCP 工具形成对比。
- 接入向量库，检索相似历史 PR。
