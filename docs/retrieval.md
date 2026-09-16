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

新增 `eval/retrieval` 两个独立跨文件配对样本：新增写入方法，构造函数分别未初始化和已初始化 map。离线脚本仅验证工具、行号和证据过滤接通，不代表模型质量提升。

```powershell
go run ./cmd/eval -cases eval/retrieval -enable-retrieval
```

在已配置 API Key 的 PowerShell 中，用同一模型和相同样本分别运行（两次命令默认生成不同时间戳报告，保存已有基线）：

```powershell
go run ./cmd/eval -cases eval/retrieval -live -runs 3 -timeout 30m
go run ./cmd/eval -cases eval/retrieval -live -enable-retrieval -runs 3 -timeout 30m
```

报告包含 `retrieval_enabled`，可核对 `dataset_hash` 相同。对比 failed_cases、precision、recall、false_positive_rate、confirmed_precision、avg_total_tokens 和 avg_latency_ms；查看 case_results 的 tools 确认模型实际调用了 retrieve_code_context。仅打开开关却没有调用，不能当作检索有效的证据。

两个样本只够冒烟：还需补充真实跨文件漏报及防误报样本，开启检索重跑主集和 holdout，确认没有退化。live 测量、真实 PR 验收及收益判断尚待执行；线上静态检查资源验收仍单独进行。
