# 开发计划

目标：边做边投，先有可演示的 MVP，再逐步加工程化和 Agent 质量。

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
- Webhook 接收 PR 事件，签名校验
- 用 GitHub API 拿 PR meta、diff、文件内容
- 调 DeepSeek 生成审查意见（先不用 Tool Calling，直接给 diff）
- 回写 PR 评论
- 简单任务表记录状态

验收：在一个测试仓库开 PR，能收到评论，任务状态能查。

## 阶段 2：工程化（1-2 周）

目标：后端面试能讲。

任务：
- RabbitMQ 接入，Webhook 后异步投递
- Worker 池消费
- Redis 分布式锁，同一 PR 幂等
- MySQL 任务/结果/审计表完善
- 状态机 + 失败重试 + 死信队列
- token 成本统计
- 限流

验收：重复发同一 PR 事件不重复审查；失败可重试；审计日志可查。

## 阶段 3：Agent 质量（1-2 周）

目标：Agent 面试能讲。

任务：
- Tool Calling 框架 + 工具注册
- 实现 get_pr_meta / list_changed_files / read_diff / read_file_context / get_commit_history
- tree-sitter 函数级上下文裁剪
- 结构化输出：bug/performance/style/security
- 基础评测集 + 准确率/误报率统计

验收：Agent 能多步调工具；审查意见结构化；评测有指标。

## 阶段 4：交付（几天）

- Docker Compose 一键部署
- README：架构图、运行方式、demo 截图
- 录一个 30 秒演示视频
- 整理面试问答点

验收：别人按 README 能跑起来；面试时能讲清每个模块。
