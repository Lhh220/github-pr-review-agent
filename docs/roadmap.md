# 开发计划

目标：先有可演示的 MVP，再逐步加工程化和 Agent 质量。

## 阶段 0：项目初始化（0.5 天）

- 初始化 Go module：`github-pr-review-agent`
- 搭目录结构：cmd / internal / pkg / docs
- Docker Compose 起 Redis、RabbitMQ、MySQL
- 配置管理：环境变量 + config.yaml
- 基础日志和统一错误处理

验收：`docker compose up` 能起依赖，服务能启动空路由。

## 阶段 1：MVP（1 周）

目标：跑通“PR 事件 -> 审查 -> 回写评论”主链路。

任务：
- [x] Webhook 接收 PR 事件，签名校验
- [x] 用 GitHub API 拿 PR meta、diff、文件内容
- [x] 调 DeepSeek 生成审查意见（先不用 Tool Calling，直接给 diff + 变更文件上下文）
- [x] 回写 PR 评论
- [x] MySQL 简单任务表记录状态
- [x] `/tasks`、`/tasks/:id` 查询任务状态

验收：在一个测试仓库开 PR，能收到评论，任务状态能查。

当前状态：已完成。线上链路已验证能回写 PR Review；本地 MySQL 已验证任务创建、去重、状态流转和查询。

## 阶段 2：工程化（1-2 周）

目标：后端面试能讲。

任务：
- [x] Day 1：结构化审查输出，`review_result` 落库，token / 耗时统计，`/tasks/:id/result` 查询
- [x] Day 2：RabbitMQ 异步队列，publisher confirm，manual ack，Worker Pool，`queued` 状态，连接断开自动重连
- [x] Day 3：失败重试、死信队列、完整状态机（代码和本地 MySQL 验证完成，待线上 RabbitMQ 验收）
- [ ] Day 4：Redis 分布式锁、限流、同一 PR 并发控制
- [ ] Day 5：审计表、观测统计、部署和文档收尾

验收：重复发同一 PR 事件不重复审查；失败可重试；审计日志可查。

## 阶段 3：Agent 质量（1-2 周）

目标：Agent 面试能讲。

任务：
- Tool Calling 框架 + 工具注册
- 实现 get_pr_meta / list_changed_files / read_diff / read_file_context / get_commit_history
- 新增 search_references，检索被删除或改名符号的跨文件引用
- 新增 run_static_checks，执行 go test / go vet 并回传结果
- tree-sitter 函数级上下文裁剪
- 结构化输出：bug/performance/style/security
- 结论分级：confirmed / needs_verification
- 基础评测集 + 准确率/误报率统计

验收：
- Agent 能多步调工具
- 能读取文件上下文并检索跨文件引用
- 能把编译失败定位到具体文件和行号
- 审查意见区分 confirmed 和 needs_verification
- 评测有指标

回归样本：

- 删除 `Config.MaxDiffLines` 字段，但保留 `cmd/server/main.go` 中的引用。
- 期望 Agent 输出：
  ```text
  internal/config/config.go 删除了 MaxDiffLines，
  但 cmd/server/main.go:40 仍在引用 cfg.MaxDiffLines，
  go test ./... 会编译失败
  ```

## 阶段 4：交付（几天）

- Docker Compose 一键部署
- README：架构图、运行方式、demo 截图
- 录一个 30 秒演示视频
- 整理面试问答点

验收：别人按 README 能跑起来；面试时能讲清每个模块。
