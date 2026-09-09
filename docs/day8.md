# Day 8 验收与交付材料

## 验收状态

| 项目 | 状态 | 结果 |
| --- | --- | --- |
| 本地全量回归 | 已完成 | `go test -p 1 ./...` 通过 |
| 本地静态检查 | 已完成 | `go vet ./...` 通过 |
| 离线评测 | 已完成 | 5 cases，precision / recall / confirmed precision 均为 1.0 |
| 线上健康检查 | 已完成 | 2026-09-09 `GET /healthz` 返回 `{"status":"ok"}` |
| Day 7 线上冒烟 | 已完成 | PR #23 / Task 24 生成结构化 review，4 条 finding 均带 evidence 并标记 `needs_verification` |
| Evidence 精确校验 | 已完成 | raw diff / file context / JSON 工具输出均按 file + line + exact line 校验 |
| Live 模型评测 | 待执行 | 本地未配置 `DEEPSEEK_API_KEY`，不能伪造统计结果 |
| 静态检查线上专项 | 待执行 | 需要在 Railway 开启配置并提交编译错误 PR |

## 剩余线上操作

1. 配置本地环境变量后运行 live 评测：

   ```powershell
   $env:DEEPSEEK_API_KEY="..."
   go run ./cmd/eval -live -report eval/report-live.json
   ```

2. 在 Railway 设置：

   ```text
   AGENT_MODE=tool_calling
   AGENT_ENABLE_STATIC_CHECKS=true
   AGENT_TOOL_TIMEOUT=3m
   AGENT_STATIC_CHECK_TIMEOUT=2m
   AGENT_STATIC_CHECK_GOPROXY=https://goproxy.cn,direct
   ```

3. 提交一个引入编译错误的 PR，确认 bot 评论引用 `go test ./...` 或 `go vet ./...` 的失败输出。
4. 用任务 ID 请求 `/tasks/<task_id>/tool-calls`，确认 `run_static_checks` 的输入、输出和耗时已落库。

## 简历表述

可选中文表述：

- 基于 Go、DeepSeek Tool Calling 和 GitHub App 实现 PR 自动审查系统，覆盖 Webhook 签名校验、RabbitMQ 异步任务、指数退避重试、死信队列、Redis 分布式锁与限流、MySQL 状态机、审计日志和开发者后台。
- 实现 7 个 Agent 工具，包括 PR diff、tree-sitter 函数级上下文裁剪、跨文件引用检索和服务端白名单静态检查；审查结果强制携带 evidence，并区分 `confirmed` 与 `needs_verification`。
- 设计离线可回归评测集，统计 precision、recall、误报率、分类准确率、confidence 校准、token 成本、延迟和工具调用轨迹；使用脚本化模型响应驱动真实 Agent 工具链，实现零成本 CI 回归。

可选英文表述：

- Built a Go-based GitHub PR review agent with DeepSeek tool calling, GitHub App integration, RabbitMQ asynchronous workers, retry/DLQ handling, Redis distributed locks and rate limiting, MySQL state tracking, audit logs, and an embedded admin console.
- Implemented seven agent tools including diff inspection, tree-sitter function context extraction, cross-file reference search, and allowlisted static checks; enforced evidence-backed findings with confirmed/needs-verification confidence levels.
- Created a deterministic offline evaluation harness that exercises the real agent toolchain with scripted model responses and reports precision, recall, false-positive rate, category accuracy, confidence calibration, token cost, latency, and tool traces.

## 面试叙事

30 秒版本：

这个项目把 GitHub PR event 变成异步审查任务。Webhook 校验签名和 delivery id 后写入 MySQL，再投递 RabbitMQ；Worker 在 Redis PR 级锁保护下执行审查。审查链路支持 legacy 固定上下文和 tool calling 两种模式。Agent 有 7 个工具，可以读取 diff、函数上下文、提交历史、跨文件引用和静态检查结果。输出是结构化 JSON，每条 finding 必须携带 evidence，并区分 confirmed 和 needs_verification。最后用离线评测集统计 precision、recall、误报率、confidence 校准和工具轨迹，方便回归。

关键取舍：

- **为什么用 RabbitMQ？** 审查耗时不可控，Webhook 必须快速返回；同时需要 manual ack、publisher confirm、延迟重试和死信队列。
- **为什么用 Redis 锁？** 同一个 PR 的 opened/synchronize/reopened 事件可能并发到达，MySQL 状态机能去重和 claim，Redis 锁进一步避免同一个 PR 并发审查。
- **为什么用 tarball 做引用检索？** GitHub code search 不能稳定搜索 PR head；逐文件调用 GitHub API 配额高，tarball 可以一次下载并流式限额扫描。
- **为什么 evidence 校验在服务端做？** 模型可能编造文件、行号或输出；服务端必须校验 file、line 和 exact source，不能只相信 JSON 格式。
- **为什么 performance 强制 needs_verification？** 当前没有 benchmark 工具，源码只能提示风险，不能证明性能退化；等加入 benchmark 工具后再允许 confirmed。
- **为什么评测默认离线？** 离线脚本驱动真实工具链，能在 CI 中稳定回归工具协议、evidence 校验、confidence 降级和 docs-only 行为，不消耗模型费用。
