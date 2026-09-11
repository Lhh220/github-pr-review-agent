# 交付验收与材料

## 2026-09-11 本轮交付增量

- 已修复：证据位置纠正（复用一次最终验证重试）；纠正后仍进行原来的精确引用校验；虚构证据不接受。
- 已修复：候选被过滤时使用中性摘要，保留原文用于诊断；不再追加“无问题”结论。
- Go PR 在模型调用前读取根 go.mod；嵌套模块仍需工具进一步确认。缺少文件会留下工具错误，不假设旧版本。
- 发现并修复非语法树文件的上下文范围超过实际行数时的越界；增加回归测试。
- 评测显示样本开始/结束、模型调用、纠正标记和每 20 秒等待提示；默认用时间戳文件名，拒绝覆盖已有报告。
- 主集 004 改为 bug/high/confirmed，原因是明确违反每个任务执行一次的约定。不是重新解释旧报告；新报告有新的 dataset_hash。
- 008 增加精确路径、diff 行前缀和多文件分隔的可执行测试，并在本机实际通过；这证明那些具体反例不成立，不意味着模型已不再误报。
- 新增 eval/holdout 的 4 个独立样本，单独运行、单独报告，尚未用于 live 调优。
- 静态检查输出在运行时限为 16 KiB，保留截断标记；Linux 超时取消整个进程组，Windows 不宣称具有同等进程树隔离。
- 阶段四配置与文档见 [快速交付指南](quickstart.md)。完整 Docker 启动、远端 CI 结果、真实视频与线上静态专项仍待验收，不能标为完成。

本轮验收命令：

```powershell
go test -p 1 ./...
go vet ./...
go run ./cmd/eval -runs 3
go run ./cmd/eval -cases eval/holdout -runs 3
docker compose --env-file compose.env.example config --quiet
```

本轮 live 复验（自动生成新报告，避免覆盖）：

```powershell
go run ./cmd/eval -live -runs 3 -timeout 30m
go run ./cmd/eval -cases eval/holdout -live -runs 3 -timeout 30m
```

程序结束时会打印报告路径。读取打印出的实际文件路径；下面历史示例的 `eval/report-live.json` 是原基线，不会被自动更新。

静态检查线上专项需要保存四类记录：真实成功、实际代码导致的编译/测试失败、超时、环境/依赖失败。最后两类只能标记验证不完整；任务失败、工具调用失败、检查命令失败是不同层次。至少记录 PR/head、Task ID、命令、退出码、输出、超时状态、最终 finding。远端 CI 的 Linux 进程组测试与部署冒烟也需确认通过。


## 验收状态

| 项目 | 状态 | 结果 |
| --- | --- | --- |
| 本地全量回归 | 已完成 | `go test -p 1 ./...` 通过；2026-09-10 使用独立临时 MySQL 8.0.33 验证数据库迁移和发布事务 |
| 本地静态检查 | 已完成 | `go vet ./...` 通过 |
| 离线评测 | 已完成 | 9 cases × 3 轮，precision / recall / confirmed precision 均为 1.0；仅代表脚本回归 |
| 线上健康检查 | 已完成 | 2026-09-09 `GET /healthz` 返回 `{"status":"ok"}` |
| Day 7 线上冒烟 | 已完成 | PR #23 / Task 24 生成结构化 review，4 条 finding 均带 evidence 并标记 `needs_verification` |
| Evidence 精确校验 | 已完成 | raw diff / file context / JSON 工具输出均按 file + line + exact line 校验 |
| 收尾代码补强 | 本地完成，待部署 | 无效模型输出报错重试；静态证据拒绝空摘录、成功、超时与启动错误；新增正常代码负样本、评测失败记录与逐样本报告 |
| 评论发布恢复 | 本地完成，待部署 | 20 个模式/故障组合验证不重复分析或发评；发布凭证与 done、审计同事务；migration 4 实库通过 |
| CI | 配置已添加，远端待运行 | push / PR 运行测试、MySQL 集成、vet、离线评测；不使用模型 Key |
| Live 模型评测 | 已有基线，本轮复测待执行 | 2026-09-11 用户实际运行：27/27，0 失败，precision=0.50，recall=0.60，负样本误报率=0.25 |
| 静态检查线上专项 | 待执行 | 需要在 Railway 开启配置并提交编译错误 PR |

## 剩余验收操作

### 0. 部署本轮发布恢复修复

提交并推送本轮修改，确认 CI 成功、Railway 完成部署，并在启动日志确认 migration 4 `review_delivery` 已应用。无须新增生产环境变量。然后提交一个测试 PR，在 `/tasks/<id>/result` 核对 `delivery.github_review_id` 大于零、`delivery.commit_sha` 与评论版本一致，任务为 done。

本地故障注入覆盖 legacy / agent / 两种 docs-only 路径：POST 未送达、POST 已成功但响应丢失、发布后数据库写失败、结果提交回执丢失、查询评论失败。已有保存结果时，重试不再调用模型；已发出的评论通过标识和 commit 找回。固定 commit 防止将旧分析结果标成新版本；分析期间检测到 head 变化时停止，不发布混合版本结果。

注意：GitHub API 与 MySQL 之间没有共同事务，查询与 POST 仍有竞态，不能声称严格 exactly-once。连续基础设施失败仍可能耗尽尝试次数进入死信；修复后重新入队会先对账。旧任务有 result 却没有 delivery 时会提示 `manual reconciliation required`，不要删除结果或随意补造标识：先人工核对历史评论，已完成的任务由管理员确认状态；确需重新审查则通过新的 PR 事件创建新任务。

独立临时 MySQL 只用于本地测试，不是生产连接配置。已有 live 基线来自用户运行；本轮新代码尚未 live 复测。线上专项仍按下面步骤执行，不能用本地测试代替。

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
go run ./cmd/eval -live -runs 3 -timeout 30m
Remove-Item Env:DEEPSEEK_API_KEY
```

这一步消耗模型额度，使用本地模拟 PR，不会向 GitHub 发评论。默认不运行静态检查，静态检查单独在线上验收。

查看报告：

```powershell
$report = Get-Content eval/report-live.json -Raw | ConvertFrom-Json
$report | Select-Object mode,model,cases,planned_cases,failed_cases,negative_cases,precision,recall,false_positive_rate,confirmed_precision,overconfirmed_findings
$report.case_results | Where-Object error | Select-Object name,run,error
```

主集本次应执行 27 次，`mode=live`、`cases=planned_cases=27`、`failed_cases=0`、`negative_cases=12`。先排除运行失败，再逐条核对 `findings / false_positive_findings / missed_findings`；不能把位置、类别吻合当作语义正确。记录真实指标及误报/漏报例子；有问题则修复后重跑，不要求伪造满分。失败调用的费用不包含在平均 token 中。命令失败时先看报告中的 error，已完成结果仍保留，重新运行建议使用新报告文件名。

### 2. 部署修复并验收静态检查

先提交本地修复、推送到部署分支，等待 Railway 完成构建；本轮仅本地验证，尚未部署。仅在受控个人测试仓库和专用测试环境执行；不要对不可信 PR 开启本机静态执行。在 Railway 设置：

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

Task 26 后续质量检查：新增两个误报回归样本；live 报告共 27 次执行，实际代码负样本 12 次。重点核对 `008-diff-section-contract`、`009-review-output-policy` 是否仍把已有实现和明确策略报成 bug，同时检查四个正样本的 recall。离线满分不代表提示词效果已在线上验证。

Task 26 摘要中的 OOM 尚未通过原始工具日志核实。打开 `/admin` 的 Task 26，查看 `run_static_checks` 的 output，分别核对 command、output、exit_code、timed_out、error；仅有 `signal: killed` 时只能确认进程被终止，OOM 原因还需 Railway 内存指标或容器日志支持。若要确认某个包测试通过，也必须找到对应的实际命令及成功输出。资源不足不算静态检查专项验收成功。

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
