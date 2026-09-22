# 仓库检索增强（RAG 第一版）

这是一版可关闭的词法代码检索增强，不依赖 embedding 服务或向量数据库。`retrieve_code_context` 在当前 PR head SHA 的仓库快照中按多个关键词查找相关代码块，把带路径、行号的源码交给 Agent 判断。它补充现有单符号 `search_references`，不是静态分析器，也不保证找全所有调用关系。

## 实现与边界

- 复用单次审查内的 tarball 缓存，固定 head SHA；不建立跨仓库共享索引。相同参数的成功调用复用 Agent 工具缓存。
- 查询最多 256 字节、8 个不同关键词；忽略大小写，按关键词子串匹配。每命中一个正文关键词加 4 分，路径关键词加 1 分；同分按路径和起始行稳定排序。
- 每块最多 20 行、步长 16；top_k 默认 3、最大 5。输出为最多 12 KiB 的完整 JSON，超限整块舍弃，绝不伪造半行证据。行片段按现有引用接口去除首尾空白。
- 压缩快照沿用 32 MiB 下载上限；扫描解压数据最多 128 MiB（含跳过的 tar 条目），可搜索文件最多 2000 个、单文件最多 2 MiB。跳过二进制、vendor、node_modules、.git 以及 eval/report-*。不落地解压，不执行仓库代码。
- `scan_truncated` 表示扫描不完整，`results_truncated` 表示匹配候选或输出被裁剪。没有返回某符号不等于仓库中不存在。
- 所有输出继续受 48 KiB 工具累计预算和 96 KiB 请求预算限制；finding 仍须通过原有路径、行号和源码校验。检索分数不能证明缺陷，源码中的指令不可信。

当前按查询扫描快照，小仓库无需额外基础设施；大仓库、多查询或同义词召回效果不足时，再依据测量结果考虑索引或混合检索。20 行窗口可能截断函数，需要 Agent 再用 read_file_context 查看完整上下文。

## 启用

服务端须为 `AGENT_MODE=tool_calling`，并设置 `AGENT_ENABLE_RETRIEVAL=true`。默认 false，方便回退和对照。

本地 PowerShell 启动服务前：

```powershell
$env:AGENT_MODE = 'tool_calling'
$env:AGENT_ENABLE_RETRIEVAL = 'true'
```

Docker Compose 用户可在项目 `.env` 设置上述两项，然后重建 app：

```powershell
docker compose -p pr-review-demo up -d --build app
```

## 对照评测

`eval/retrieval` 现有 6 个独立样本（3 组正反例），全部包含 go.mod：

| 样本 | 检验内容 |
| --- | --- |
| 001 / 002 | 构造函数是否初始化 map |
| 003 / 004 | 多个包有同名 Validate，必须检查调用所在包的校验是否排除零 |
| 005 / 006 | 算术位于长函数尾部，Top-1 未包含前面的保护逻辑，需继续读上下文 |

最后一组使用明确标注的填充注释构造窗口边界，是合成回归样本，不代表真实代码复杂度。`retrieval.json` 仅用于测试指定查询是否返回关键源码行，不进入模型输入。离线测试同时检查完整上下文的补读、最终 finding 和所有工具错误；脚本不证明模型会自行选择正确查询或正确补读。

```powershell
go run ./cmd/eval -cases eval/retrieval -enable-retrieval
```

在已配置 API Key 的 PowerShell 中，用同一模型和相同样本分别运行（两次命令默认生成不同时间戳报告，保存已有基线）：

```powershell
go run ./cmd/eval -cases eval/retrieval -live -runs 3 -timeout 30m
go run ./cmd/eval -cases eval/retrieval -live -enable-retrieval -runs 3 -timeout 30m
```

报告包含 `retrieval_enabled`，可核对 `dataset_hash` 相同。对比 failed_cases、precision、recall、false_positive_rate、confirmed_precision、avg_total_tokens 和 avg_latency_ms；查看 case_results 的 tools 确认模型实际调用了 retrieve_code_context。仅打开开关却没有调用，不能当作检索有效的证据。

## 已完成的旧版 live 基线

2026-09-16，旧版两个跨文件样本各跑 3 次，开关两组均 6/6 完成，precision/recall/confirmed_precision 为 1，误报率 0。实验组 6 次均成功调用新工具。平均 token 从 14208.5 增至 14552（+2.4%），平均耗时从 4921 ms 降至 4367.5 ms（-11.2%）。少量顺序运行无法证明稳定提速或准确率收益。

开启检索的主集 27/27、holdout 12/12 完成，质量指标均满分；主集调用新工具 1 次，holdout 未调用。原始报告分别为 report-20260916T074918.577781300Z.json、report-20260916T075025.412187800Z.json、report-20260916T075355.663471700Z.json、report-20260916T075545.525813600Z.json，保留在本地，不提交报告。

本轮补齐 go.mod 并扩充到 6 个样本后，dataset_hash 已变化，必须重跑两组，不能直接拿旧版 2 个样本的均值和新版比较。上述 A/B 命令现在各执行 18 次。六个合成样本仍不足以证明普遍收益，后续应加入真实跨文件漏报案例；线上静态检查资源验收继续单独进行。

## 真实 PR 部署验收（待执行）

1. 先提交本次改动，将包含检索工具的代码部署到审查服务；在部署配置中设置 AGENT_MODE=tool_calling、AGENT_ENABLE_RETRIEVAL=true。按上文重建 app，再执行：

   ```powershell
   docker compose -p pr-review-demo ps
   Invoke-RestMethod http://localhost:8080/healthz
   ```

   healthy 和 healthz=ok 只代表服务就绪，不代表检索已验收。

2. 在 GitHub App 已安装的测试仓库创建专用分支和 PR，标明“验收专用，请勿合并”。可使用 003 样本的 quota/share.go、quota/validate.go 以及同名干扰文件，保留真实项目的 go.mod。文件源码可从 fixture/case.json 的 file_contents 和 repository 字段取得；不要把 JSON 本身当作待审查代码。
3. 在 PR 描述要求检查 Share 的调用链，并使用 retrieve_code_context 检索相关校验。等待任务完成后，检查工具记录确实调用成功，输出 ref 等于任务 head SHA、路径/行号/源码准确，报告定位到实际除零语句。若模型未调用工具，只能认定普通审查成功，不能认定检索验收通过。
4. 把 quota.Validate 从 count >= 0 改成 count > 0 并 push。核对新任务指向新 SHA，工具返回更新后的防护代码，不能继续发布旧的除零结论。保留前后任务 ID、commit、工具输出与最终评论作为验收记录。
5. 另用 006 长函数反例验证防误报：工具只检索出除法片段后，应继续读完整函数，看到空输入保护，最终不发布除零 finding。记录扫描/输出截断标记以及是否因预算而未完成补读；信息不足不能算无缺陷证明。
6. 检查 CI 通过后关闭验收 PR，不合并探针代码。如果需要回退检索，设 AGENT_ENABLE_RETRIEVAL=false 并重建 app。

本轮只完成本地样本与回归，不自动发布 PR、修改线上配置或使用 API Key 运行付费模型。真实 PR 验收完成后，再根据漏检原因决定是否优化排序、分块或引入向量检索。

## 定位与离线重评分

预期 finding 可用 `accepted_locations` 明确列出合理备选位置；备选必须精确匹配文件和行号，不能把整个文件设为通过。005 的新增生产调用方 api/average.go:6 可暴露同一个除零问题，因此列为备选；001 的测试示例不列为备选。重复报告同一缺陷仍只匹配一次。

precision/recall 使用首选或显式备选位置并核对 category；location_precision/location_recall 保留原有首选位置规则（同文件、行号 ±3），不因备选而提高。两种指标都属于位置匹配评分，不等同于自动语义审查。新版 scoring_version 为 explicit-locations-v2。

可以不调用模型，使用新预期重新评分已有输出：

```powershell
go run ./cmd/eval -cases eval/retrieval -rescore eval/report-20260916T080945.616586900Z.json
go run ./cmd/eval -cases eval/retrieval -rescore eval/report-20260916T081207.012777600Z.json
```

默认写入新时间戳文件，不覆盖原始报告。rescored_from 和 source_dataset_hash 保存来源；dataset_hash 指向当前评测集。该操作只应用当前预期，不重新执行工具或验证证据，也不验证历史 fixture 与当前 fixture 是否完全相同；只应对已人工核对的预期变更使用，不能当作新提示词的 live 结果。错误用例仍保留失败状态，不隐藏失败。

Agent 提示词同时明确：优先定位实际失败操作或缺失防护的生产位置，必要时允许新增生产调用方；不要因测试示例能触发故障就把它作为主要定位，也不得把未修改文件称为新增。提示词效果需后续同模型 A/B live 验证，重评分不能证明定位已修复。
