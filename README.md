<h1 align="center">🔔 Alertmanager Webhook Notifier</h1>
<p align="center">将 Prometheus Alertmanager 告警信息转发到企业微信 / 钉钉 / 飞书等平台的高可用 Webhook 服务</p>

---

## ✨ 项目简介

本项目是一个用于对接 Prometheus Alertmanager 的 Webhook 转发程序，基于 Gin 和 Go 编写，支持将告警信息转化为富格式 markdown 消息并推送到：

- ✅ 企业微信（WeCom）
- ✅ 钉钉（DingTalk）
- ✅ 飞书（Feishu）

> 可用于企业告警系统的统一消息通知中心。


## ✨ 功能特点

- 📢 支持多平台告警转发（企业微信 / 钉钉 / 飞书）
- 🎨 告警消息内容美化，支持 Markdown 格式
- 🧩 支持按平台灵活配置 webhook
- 🛡️ 启动时自动校验配置文件完整性
- 🚀 基于 Gin 的高性能 Web 服务

## 📦 使用方式

### 1. 构建与运行

```bash
# 编译(这里以编译到 Linux操作系统)
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o kube-alert
```

# 运行（默认读取 ./config.yaml）
使用scripts目录下的 start.sh 脚本启动

### 2. 示例配置文件（config.yaml）

```yaml
server:
  port: "0.0.0.0:18082"

# 消息接收客户端，支持配置数组同时发送，配置客户端需要同时对应的webhook，否则程序会异常
client:
  - wechat
  - dingtalk

notifiers:
  wechat:
    webhook_url: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=xxxxxxxxxxxxxxxxxxxxxx"
  dingtalk:
    webhook_url: "https://oapi.dingtalk.com/robot/send?access_token=xxxxxxxxxxxxxxxxxxxxxxxxx"
  feishu:
    webhook_url: "https://open.feishu.cn/open-apis/bot/v2/hook/xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"

```

### 3. Alertmanager 配置

```yaml
receivers:
  - name: 'webhook-notifier'
    webhook_configs:
      - url: 'http://your-server-ip:8080/webhook-alert'
```

## 📜 API

- `POST /webhook-alert`：接收 Alertmanager 的 webhook 请求并转发消息

## 🖼️ 告警效果预览

> Markdown 格式美化后的消息（支持平台不同展示可能略有差异）

- 🔴 **告警中 (FIRING)**：显示红色图标 + 详细告警内容 + 时间戳
- ✅ **已恢复 (RESOLVED)**：显示绿色图标，仅展示标题和恢复时间

## 🧰 开发与调试

- 本地调试推荐使用 `ngrok` 或 `frp` 暴露本地端口供 Alertmanager 访问
- 日志采用标准输出，建议使用 Docker 部署并配合日志采集组件使用

## 📄 License

MIT License

---
