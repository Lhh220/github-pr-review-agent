# 真实漏查回归

来源：PR #31 / Task 41。保留原测试和修复测试作为正反例，生产代码相同。fixture 将测试文件展示为新增以独立复现审查场景，属于真实案例改编，不是原 PR 的逐字快照。001 的模拟服务器没有接入请求路径，任意外部错误都可能通过断言；002 重定向请求、确认服务器收到请求并核对 deadline 错误。

此集合参与提示词改进，不能称为独立 holdout。离线脚本只验证工具与证据评分链路，不证明模型会发现缺陷。Live 时需人工核对结论是否真正指出请求未接入 mock。

```powershell
go run ./cmd/eval -cases eval/review-regressions
go run ./cmd/eval -cases eval/review-regressions -live -runs 3 -timeout 15m
```
