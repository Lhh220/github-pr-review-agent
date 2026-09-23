# Mock 接线独立验证集

六个手工合成样本，三组正反例：base URL、对象依赖注入、DialContext Transport。样本结构不同于原令牌测试，但属于已知同类缺陷，不能当作广泛真实 PR 泛化证明。当前没有使用这些样本的 live 输出调整提示词或标签；一旦据此调优，须将本集转为回归集。

| 样本 | 预期 |
|---|---|
| 001 | 缺失 base URL，仍请求默认地址 |
| 002 | base URL 指向本地服务器，断言收到请求和 deadline |
| 003 | mock 安装到一个 Service，却调用另一个实例 |
| 004 | 调用已注入 mock 的实例，核对命中和 503 错误 |
| 005 | DialContext 配置在克隆 Transport，客户端却使用默认 Transport |
| 006 | 使用带 DialContext 的 Transport，核对命中和 deadline |

缺陷定位限定在新增测试的接线处，生产默认地址不作为备选。正常样本明确验证 mock 被调用。脚本仅驱动离线工具/证据/评分链路，不是 live 模型推理成绩。缺陷版使用 service.invalid，不应直接联网执行；编译验证使用 go test -run '^$'，正常版可在本机运行（仅 loopback/内存 Transport）。

```powershell
go run ./cmd/eval -cases eval/mock-wiring-holdout
go run ./cmd/eval -cases eval/mock-wiring-holdout -live -runs 3 -timeout 30m
```

与旧回归集分开报告：检查执行完成、误报漏报、定位和措辞。即使分数匹配，也须人工确认没有将可能的网络错误描述为实际观测、没有断言移除超时必定通过。保留原报告和 dataset_hash，不根据模型答案修改预期来提高分数。
