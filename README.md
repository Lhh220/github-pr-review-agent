# GitHub PR 智能代码审查 Agent

一个基于 Go 的 GitHub PR 自动审查 Agent。收到 GitHub PR 事件后，自动读取 PR 上下文和 diff，调用 LLM 生成审查意见，并回写 PR 评论。

## 当前进度

当前是 MVP 版本，先跑通最小闭环：

- 接收 GitHub Webhook
- 校验签名
- 读取 PR 信息和 diff
- 调用 DeepSeek 生成审查意见
- 回写 PR 评论

后续会逐步加入 RabbitMQ、Redis 分布式锁、状态机、Tool Calling 和 tree-sitter 上下文裁剪。

## 本地运行

1. 准备环境变量：

```powershell
$env:GITHUB_WEBHOOK_SECRET="你的webhook secret"
$env:GITHUB_TOKEN="你的 GitHub token"
$env:DEEPSEEK_API_KEY="你的 DeepSeek API key"
```

如果使用 GitHub App，也可以配置：

```powershell
$env:GITHUB_APP_ID="你的 App ID"
$env:GITHUB_APP_PRIVATE_KEY="你的私钥内容"
$env:GITHUB_APP_PRIVATE_KEY_PATH="D:\path\to\private-key.pem"
$env:GITHUB_INSTALLATION_ID="你的 installation ID"
```

2. 启动服务：

```powershell
go run ./cmd/server
```

3. 用 ngrok 暴露本地端口：

```powershell
ngrok http 8080
```

4. 在 GitHub App 或仓库 Webhook 里把 `POST /webhook/github` 指向 ngrok 地址。

## 说明

- `GITHUB_TOKEN` 适合本地快速调试。
- 正式演示建议用 GitHub App，评论会以独立 bot 身份发出。
