# GitHub PR 智能代码审查 Agent 设计文档

## 1. 项目目标

做一个基于 Go 的 GitHub PR 自动审查 Agent。接收 GitHub PR 事件后，异步执行代码审查，通过 Tool Calling 读取 diff、文件上下文、历史提交，生成结构化审查意见并回写 PR 评论。

定位：个人项目，面向后端开发 / Agent 开发岗位面试，重点体现 Agent 工程化和 Go 后端能力，不是套壳 LLM。

## 2. 核心价值

- 后端：Webhook 接入、异步任务队列、并发控制、分布式锁、状态机、审计、限流。
- Agent：Tool Calling 框架、上下文裁剪、结构化输出、评测集、token 成本控制。
- 真实可跑：能部署成 GitHub App，在自己仓库上演示。
- test

## 3. 整体架构

```text
GitHub PR Event
   |
   v
Webhook Receiver (Gin)
   |  去重 / 签名校验 / 限流
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
MySQL (任务/结果/审计) + Redis (锁/去重/限流)
```

## 4. 模块设计

### 4.1 Webhook Receiver

- 路由：`POST /webhook/github`
- 校验 GitHub 签名（X-Hub-Signature-256）。
- 解析 PR 事件，只处理 `opened / synchronize / reopened`。
- 生成 taskId，幂等写入 MySQL（repo + pr_number + commit_sha 唯一），避免重复投递。
- 投递到 RabbitMQ 后立即返回 202。
- 限流：基于 Redis 按仓库维度限流，防止 webhook 洪水。

### 4.2 任务队列

- 队列：`pr.review.queue`，配合死信队列。
- 消息体只放 taskId，具体数据从 MySQL 取，避免消息体过大。
- 消费失败重试：指数退避，达到最大次数后进死信队列。

### 4.3 Worker Pool

- 固定大小 goroutine 池消费队列。
- 消费前用 Redis 分布式锁：`lock:pr:{repo}:{number}`，TTL 可配置。
- 拿不到锁说明同一 PR 正在审查，跳过或等待。
- 任务结束后释放锁，更新任务状态。

### 4.4 Agent Loop

- 输入：任务上下文（仓库、PR、commit）。
- Agent 执行多步推理：
  1. 调 `get_pr_meta` 了解 PR 标题、描述、改动文件列表。
  2. 调 `read_diff` 读取完整 diff。
  3. 对关注文件调 `read_file_context`，用 tree-sitter 取函数级上下文。
  4. 必要时调 `get_commit_history` 理解修改动机。
  5. 输出结构化审查意见。
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

### 4.7 审查结果

结构化输出：
```json
{
  "summary": "整体评价",
  "reviews": [
    {
      "category": "bug|performance|style|security",
      "file": "path",
      "line": 12,
      "severity": "high|medium|low",
      "comment": "具体问题",
      "suggestion": "修改建议"
    }
  ]
}
```
回写策略：按文件分组，生成一条总结评论 + 多条行内评论；无问题时可发一条通过评论。

### 4.8 存储设计

MySQL 表：
- `review_task`：id, repo, pr_number, commit_sha, status, created_at, updated_at, error。
- `review_result`：id, task_id, summary, payload_json, created_at。
- `tool_call_log`：id, task_id, tool_name, input_json, output_json, tokens, duration_ms, created_at。
- `audit_log`：id, task_id, action, actor, detail_json, created_at。

Redis Key：
- `lock:pr:{repo}:{number}`：分布式锁。
- `dedup:pr:{repo}:{number}:{sha}`：事件去重。
- `ratelimit:webhook:{repo}`：限流。

### 4.9 状态机

```text
received -> queued -> running -> formatting -> commenting -> done
                         |          |            |
                         v          v            v
                       failed    failed       failed
```
任何阶段失败都记 error 并更新状态；支持从 failed 重试，最多 N 次。

## 5. 技术选型

| 组件 | 选型 | 理由 |
---|---|---|
| 语言 | Go | 简历主语言，并发模型适合 Worker |
| HTTP | Gin | 已有栈，中间件鉴权/限流方便 |
| 队列 | RabbitMQ | 面试常问，支持死信、重试 |
| 缓存/锁 | Redis | 分布式锁、去重、限流 |
| 数据库 | MySQL + GORM | 任务/结果/审计持久化 |
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
2. Receiver 校验签名、解析事件、去重，写 `review_task(received)`，投递 RabbitMQ。
3. Worker 消费消息，加 Redis 锁，状态置 `running`。
4. Agent 调工具读取 PR meta、diff、文件上下文、历史提交。
5. Context Builder 用 tree-sitter 裁剪上下文，控制在 token 预算内。
6. LLM 生成结构化审查意见，记录每次 tool_call 到日志表。
7. 状态置 `formatting` -> `commenting`，回写 GitHub PR 评论。
8. 状态置 `done`，释放锁，记录审计。

## 7. 安全与成本

- Webhook 签名校验，防止伪造。
- 工具调用只读，不执行写操作（除回写评论）。
- Redis 限流，防止同仓库洪泛。
- token 成本统计：每次 LLM 调用记录 input/output tokens，落库。
- 超时控制：每个任务最大执行时间，防止卡死。

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
