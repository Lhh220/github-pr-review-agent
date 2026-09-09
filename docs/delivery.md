# 交付验收与材料

## 验收状态

| 项目 | 状态 | 结果 |
| --- | --- | --- |
| 本地全量回归 | 已完成 | `go test -p 1 ./...` 通过；当前未配置 MYSQL_DSN，数据库集成测试跳过 |
| 本地静态检查 | 已完成 | `go vet ./...` 通过 |
| 离线评测 | 已完成 | 7 cases × 3 轮，precision / recall / confirmed precision 均为 1.0；仅代表脚本回归 |
| 线上健康检查 | 已完成 | 2026-09-09 `GET /healthz` 返回 `{"status":"ok"}` |
| Day 7 线上冒烟 | 已完成 | PR #23 / Task 24 生成结构化 review，4 条 finding 均带 evidence 并标记 `needs_verification` |
| Evidence 精确校验 | 已完成 | raw diff / file context / JSON 工具输出均按 file + line + exact line 校验 |
| 收尾代码补强 | 本地完成，待部署 | 无效模型输出报错重试；静态证据拒绝空摘录、成功、超时与启动错误；新增正常代码负样本、评测失败记录与逐样本报告 |
| Live 模型评测 | 待执行 | 本地未配置 `DEEPSEEK_API_KEY`，不能伪造统计结果 |
| 静态检查线上专项 | 待执行 | 需要在 Railway 开启配置并提交编译错误 PR |

## 剩余验收操作

### 1. 本机运行 live 评测

Windows PowerShell 在项目根目录执行。当前机器可用的 C 编译器如下；其他机器替换 CC 路径：

```powershell
$env:GOCACHE="$PWD\.gocache"
$env:GOMODCACHE="$PWD\.gomodcache"
$env:GOPATH="$PWD\.gopath"
$env:CGO_ENABLED="1"
$env:CC="D:\Dev-Cpp\TDM-GCC-64\bin\gcc.exe"
$evalKey = Read-Host "DeepSeek API Key" -AsSecureString
$env:DEEPSEEK_API_KEY = [System.Net.NetworkCredential]::new("", $evalKey).Password
go run ./cmd/eval -live -runs 3 -timeout 30m -report eval/report-live.json
Remove-Item Env:DEEPSEEK_API_KEY
```

这一步消耗模型额度，使用本地模拟 PR，不会向 GitHub 发评论。默认不运行静态检查，静态检查单独在线上验收。

查看报告：

```powershell
$report = Get-Content eval/report-live.json -Raw | ConvertFrom-Json
$report | Select-Object mode,model,cases,planned_cases,failed_cases,negative_cases,precision,recall,false_positive_rate,confirmed_precision,overconfirmed_findings
$report.case_results | Where-Object error | Select-Object name,run,error
```

本次应执行 21 次，`mode=live`、`cases=planned_cases=21`、`failed_cases=0`、`negative_cases=6`。先排除运行失败，再逐条核对 `findings / false_positive_findings / missed_findings`；不能把位置、类别吻合当作语义正确。记录真实指标及误报/漏报例子；有问题则修复后重跑，不要求伪造满分。失败调用的费用不包含在平均 token 中。命令失败时先看报告中的 error，已完成结果仍保留，重新运行建议使用新报告文件名。

### 2. 部署修复并验收静态检查

先提交本地修复、推送到部署分支，等待 Railway 完成构建；本轮仅本地验证，尚未部署。在 Railway 设置：

```text
AGENT_MODE=tool_calling
AGENT_ENABLE_STATIC_CHECKS=true
AGENT_TOOL_TIMEOUT=3m
AGENT_STATIC_CHECK_TIMEOUT=2m
AGENT_STATIC_CHECK_GOPROXY=https://goproxy.cn,direct
```

在已安装 GitHub App 的个人测试仓库，新建测试分支及 PR。例如新增 `phase3_probe.go`（package 与所在目录一致）：

```go
package main

var phase3Probe = phase3UndefinedSymbol
```

PR 描述说明“请调用 run_static_checks，使用 go_test 验证编译结果”。不要合并此 PR。若用独立测试仓库，仓库根目录须有兼容 Go 1.25 的 go.mod。

bot 回评后，在 `/admin` 根据评论里的 Task ID 查看任务详情：

1. 任务为 done，并有 `run_static_checks` 工具日志；需要检查日志内部 output JSON，而非只看外层工具 status。
2. `checks` 中对应命令为 `go test ./...` 或 `go vet ./...`，`success=false`、`exit_code>0`、`timed_out=false`、无 error，output 含 undefined 编译错误。
3. review finding 的 `evidence.type=static_check`，command 和非空 excerpt 对应上述真实失败输出。
4. 修正测试分支（例如改为 `var phase3Probe = 1`）再推送，确认新的任务不再报告该编译错误，最后关闭测试 PR。

依赖下载失败、超时或未调用工具都不算通过。首次下载慢可在个人测试仓库重试；本工具仍是受限本地执行，不能视为强隔离沙箱。验收完可将 `AGENT_ENABLE_STATIC_CHECKS=false`，保留 tool_calling；需要整体回滚时将 `AGENT_MODE=legacy`。

### 3. 保存验收证据后收官

记录 live 报告文件、实际模型、指标、测试 PR、前后两个 Task ID、静态检查 output 和 review evidence，然后更新本页状态并勾选 roadmap 的 Day 8 专项补充。历史 PR #23 冒烟不代表这次新增修复已上线。

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
