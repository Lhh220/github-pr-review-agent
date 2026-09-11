# Live 评测修复与复验

> 最新增量：证据位置不一致现已进入同一个最多一次纠正流程；Go PR 预读根 go.mod；004 标签已校准，008 补充可执行反例测试。主集与新增独立集分开评测，见 [交付验收](delivery.md)。CLI 现拒绝覆盖既有报告，不指定 `-report` 自动生成时间戳文件。


## 第二轮格式纠正补充

第二轮 27 次尝试中 9 次失败：8 次在 JSON 前输出英文解释，另 1 次 JSON 引号无效。仅收紧解析和增加提示词不足以维持可用性。

现在 tool-calling Agent 在最终响应不符合 schema 时，保留已有对话和工具结果，最多请求一次格式纠正，不提供工具。普通结束与工具预算耗尽两条路径共用此逻辑。新响应仍需通过 schema 和证据校验；再次无效则报错。额外调用计入用量和耗时，eval 保留原始及纠正后的响应。legacy 固定上下文模式未添加本次局部纠正。

本地全量测试、vet、27 次离线评测通过，尚未进行本次修改后的 live 验收。第二轮误报率为 2/3，只有 3 个正常代码样本成功进入评分，不能与此前 12 个成功负样本直接比较。请使用新文件名复验：

```powershell
go run ./cmd/eval -live -runs 3 -timeout 30m -report eval/report-live-v3.json
```

首次报告 `eval/report-live.json` 保留原样：27 次尝试中 1 次格式失败，严格 precision=0.40、recall≈0.55。该报告没有模型原文，无法从错误信息还原失败响应的具体格式。

## 本轮修改

- 解析器只接受纯 JSON 或一个完整的 JSON Markdown 代码块，拒绝混入说明、工具标记、多个对象或尾部垃圾的响应。不再截取第一个 `{` 到最后一个 `}` 之间的任意内容。非法响应仍然失败，不会作为正常审查发布。
- 提示词明确只输出一个 JSON 对象；字符串匹配需检查精确输入，语言版本相关判断需先查版本，删除字段需追溯调用者类型。这些是针对已知失败模式的约束，效果需要 live 验证。
- 每个 case 的 `responses` 保存模型各轮返回及错误，`tools` 保存输入、输出和错误；解析失败、步数耗尽时也保留记录和 token 用量。`rejected_findings` 给出证据过滤掉的候选及原因。工具输出匹配仅证明引用真实，不证明缺陷推理成立。
- 修正 001 的配置类型来源；003 补齐可编译定义并标注 SQL 注入以外的空结果关闭缺陷；004 补齐任务定义、Go 1.25 版本和执行一次的约定。
- 原有严格指标不放宽。新增 `location_precision`、`location_recall`，仅按文件和行号容差（±3 行）进行一对一匹配，忽略类别。它们用于诊断分类偏差，不能替代语义准确率，同一位置的错误推理也可能匹配。
- `dataset_hash` 标识加载的样本、标签和脚本。数据集变化后，不能把与旧报告的分数差直接归因于模型提升。失败 case 仍不进入质量评分和平均用量，实际失败成本可从 case 记录读取。

报告包含样本源码和模型原文，分享前检查内容；不包含请求认证头。建议保留在本地，不随代码批量提交。

## 下一步

在项目目录、配置好 Go 缓存和编译器的 PowerShell 中运行。若当前窗口没有 API Key，先通过安全输入设置：

```powershell
$evalKey = Read-Host '请输入 DeepSeek API Key' -AsSecureString
$env:DEEPSEEK_API_KEY = [System.Net.NetworkCredential]::new('', $evalKey).Password
go run ./cmd/eval -live -runs 3 -timeout 30m -report eval/report-live-v2.json
```

先检查是否跑全、是否格式失败，再逐条复核误报、漏报和被过滤候选：

```powershell
$report = Get-Content eval/report-live-v2.json -Raw | ConvertFrom-Json
$report | Select-Object dataset_hash,cases,planned_cases,failed_cases,precision,recall,location_precision,location_recall,false_positive_rate,confirmed_precision
$report.case_results | Where-Object { $_.error -or $_.false_positives -or $_.false_negatives } | ConvertTo-Json -Depth 40
```

若再次失败，查看对应 case 的 `responses` 最后一轮及 `tools`，区分格式错误、工具失败、上下文缺失与证据过滤。不要仅为提高分数修改标签：新增标签必须能复现，分类歧义应单独记录。

完成这轮后，冻结样本和提示词，增加未参与本轮调优的独立 PR/负样本，复测泛化能力。离线脚本通过只代表 Agent、工具、证据校验和评分链路正常，不代表 live 模型准确率。live 验收之后，阶段三仍需在部署环境对真实 PR 验收静态检查成功、真实代码失败、超时/环境失败等路径。
