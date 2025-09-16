<h1 align="center">🔔 Alertmanager Webhook Notifier</h1>
<p align="center">将 Prometheus Alertmanager 告警信息转发到企业微信 / 钉钉 / 飞书等平台的高可用 Webhook 服务</p>
<p align="center">
  <img src="https://img.shields.io/badge/Go-1.24-blue.svg" alt="Go Version">
  <img src="https://img.shields.io/badge/Platform-Linux%20%7C%20Windows%20%7C%20macOS-lightgrey.svg" alt="Platform">
  <img src="https://img.shields.io/badge/License-MIT-green.svg" alt="License">
</p>

---

## ✨ 项目简介

本项目是一个高性能的 Prometheus Alertmanager Webhook 转发服务，基于 **Go + Gin** 框架构建，支持将告警信息智能格式化并推送到多个企业级通讯平台：

- 🚀 **企业微信（WeCom）** - 支持 Markdown 格式 + 消息自动分批
- 📱 **钉钉（DingTalk）** - 支持富文本消息和颜色标识  
- 💬 **飞书（Feishu）** - 支持文本消息推送
- 📊 **大流量告警** - 基于 ClickHouse 实时监控 Nginx 访问日志，智能检测异常大流量并自动告警

> 🎯 **核心价值**：统一告警通知中心，解决告警信息分散、格式不统一的问题，并提供智能的流量异常监控

## 🌟 核心特性

### 📊 智能流量监控
- **ClickHouse 集成**：直接查询 Nginx 访问日志，实时监控流量异常
- **智能阈值**：支持自定义请求/响应大小阈值和计数阈值
- **精准告警**：按域名和路径分组监控，提供详细的异常数据
- **定时检查**：支持配置检查间隔和时间窗口

### 📢 多平台智能推送
- **三大平台支持**：企业微信、钉钉、飞书同时推送
- **消息格式优化**：针对不同平台自动适配最佳显示格式
- **长消息处理**：企业微信自动分批发送，突破4096字节限制
- **并发推送**：多平台并行发送，提升推送效率

### 🔍 智能告警过滤
- **双维度过滤**：支持基于告警名称和告警级别的精确过滤
- **灵活规则配置**：Include/Exclude 白名单黑名单机制
- **通配符支持**：支持 `*` 通配符进行模式匹配
- **实时过滤**：内存级过滤，性能影响极小

### 🛡️ 可靠性保障
- **连通性检测**：启动时自动测试所有配置的 Webhook 连接
- **错误处理**：详细的错误日志和状态追踪
- **配置验证**：启动时自动校验配置文件完整性
- **优雅降级**：部分平台失败不影响其他平台推送

### 🔧 运维友好
- **零依赖部署**：单一可执行文件，无需额外依赖
- **灵活配置**：YAML 配置文件，支持热更新
- **日志轮转**：自动日志切割和压缩
- **高性能**：基于 Gin 框架，支持高并发请求

## 📦 快速开始

### 1. 下载和构建

```bash
# 克隆项目
git clone https://github.com/your-username/alert-webhook.git
cd alert-webhook

# 构建可执行文件
## Linux/macOS
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o alert-webhook

## Windows
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o alert-webhook.exe

## 本地构建
go build -o alert-webhook
```

### 2. 配置文件设置

创建 `config.yaml` 配置文件：

```yaml
server:
  port: "0.0.0.0:18082"

# 启用的通知客户端
client:
  - wechat
  - dingtalk
  - feishu

# 各平台 Webhook 配置
notifiers:
  wechat:
    webhook_url: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=xxxxxxxxxxxxxxxxxxxxxx"
  dingtalk:
    webhook_url: "https://oapi.dingtalk.com/robot/send?access_token=xxxxxxxxxxxxxxxxxxxxxxxxx"
  feishu:
    webhook_url: "https://open.feishu.cn/open-apis/bot/v2/hook/xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"

# ClickHouse数据库配置（大流量告警功能需要）
clickhouse:
  host: "localhost"
  port: 9000
  database: "nginxlogs"
  username: "default"
  password: ""

# 大流量告警配置
traffic_alert:
  # 是否启用大流量告警
  enabled: true
  # 检查间隔（秒）
  check_interval: 300
  # 请求大小阈值（字节），超过此值视为大请求
  request_size_threshold: 1048576   # 1MB
  # 响应大小阈值（字节），超过此值视为大响应  
  response_size_threshold: 5242880  # 5MB
  # 时间窗口（分钟），检查此时间段内的流量
  time_window: 10
  # 触发告警的大请求/响应数量阈值
  count_threshold: 5

# 告警过滤规则（可选）
filter:
  # 基于告警名称的过滤
  alert_name:
    include:
      - "HighCPU*"           # 匹配以HighCPU开头的告警
      - "*Memory*"           # 匹配包含Memory的告警
      - "DiskSpaceLow"       # 精确匹配
    exclude:
      - "*Test*"             # 排除测试告警
      - "DebugAlert"         # 排除调试告警
      
  # 基于告警级别的过滤
  severity:
    include:
      - "critical"           # 严重告警
      - "warning"            # 警告告警
      - "emergency"          # 紧急告警
    exclude:
      - "info"               # 排除信息告警
      - "none"               # 排除none级别
```

### 3. ClickHouse 环境准备（可选）

如果需要使用大流量告警功能，需要先准备 ClickHouse 环境：

```sql
-- 1. 创建数据库
CREATE DATABASE IF NOT EXISTS nginxlogs;

-- 2. 创建 nginx_access 表
CREATE TABLE nginxlogs.nginx_access
(
    `timestamp` DateTime64(3, 'Asia/Shanghai'),
    `server_ip` String,
    `domain` String,
    `request_method` String,
    `status` Int32,
    `top_path` String,
    `path` String,
    `query` String,
    `protocol` String,
    `referer` String,
    `upstreamhost` String,
    `responsetime` Float32,
    `upstreamtime` Float32,
    `duration` Float32,
    `request_length` Int32,
    `response_length` Int32,
    `client_ip` String,
    `client_latitude` Float32,
    `client_longitude` Float32,
    `remote_user` String,
    `remote_ip` String,
    `xff` String,
    `client_city` String,
    `client_region` String,
    `client_country` String,
    `http_user_agent` String,
    `client_browser_family` String,
    `client_browser_major` String,
    `client_os_family` String,
    `client_os_major` String,
    `client_device_brand` String,
    `client_device_model` String,
    `createdtime` DateTime64(3, 'Asia/Shanghai')
)
ENGINE = MergeTree
PARTITION BY toYYYYMMDD(timestamp)
PRIMARY KEY (timestamp, server_ip, status, top_path, domain, upstreamhost, client_ip, remote_user, request_method, protocol, responsetime, upstreamtime, duration, request_length, response_length, path, referer, client_city, client_region, client_country, client_browser_family, client_browser_major, client_os_family, client_os_major, client_device_brand, client_device_model)
TTL toDateTime(timestamp) + toIntervalDay(30)
SETTINGS index_granularity = 8192;
```

### 4. 启动服务

```bash
# 使用默认配置文件启动
./alert-webhook

# 指定配置文件启动
./alert-webhook -config=/path/to/your/config.yaml

# 使用脚本启动（Linux/macOS）
chmod +x scripts/start.sh
./scripts/start.sh
```

## 🔗 Alertmanager 集成

在你的 Alertmanager 配置文件中添加 Webhook 接收器：

```yaml
global:
  smtp_smarthost: 'localhost:587'

route:
  group_by: ['alertname']
  group_wait: 10s
  group_interval: 10s
  repeat_interval: 1h
  receiver: 'webhook-notifier'

receivers:
  - name: 'webhook-notifier'
    webhook_configs:
      - url: 'http://your-server-ip:18082/webhook-alert'
        send_resolved: true
```

## 📋 API 接口

### POST `/webhook-alert`

接收 Alertmanager 的 webhook 请求并转发到配置的平台。

**请求体格式**：标准的 Alertmanager Webhook 格式

**响应**：
- `200 OK`: 成功发送到所有平台
- `500 Internal Server Error`: 部分或全部平台发送失败

## 🎨 消息效果预览

### 大流量告警消息效果
```
📊 Prometheus 告警通知
请关注告警信息，相关人员请注意

状态: 告警中
告警名称: HighTrafficAlert
级别: P2
域名: test.example.com
路径: /api
摘要: 域名 test.example.com 路径 /api 发现大流量异常
描述: 在过去 10 分钟内:
• 总请求数: 125
• 大请求数量: 8 (阈值: 1.0MB)
• 大响应数量: 12 (阈值: 5.0MB)
• 平均请求大小: 2048.50 字节
• 平均响应大小: 8192000.00 字节
• 最大请求大小: 4194304 字节
• 最大响应大小: 20971520 字节

最近的大请求示例:
1. 14:25:32 POST /api/bulk-upload (请求:4194304字节, 响应:12582912字节, 耗时:0.800s)
2. 14:26:15 GET /api/export (请求:2621440字节, 响应:20971520字节, 耗时:1.500s)
3. 14:27:08 GET /api/download (请求:1572864字节, 响应:15728640字节, 耗时:2.100s)
触发时间: 2025-09-16 14:30:00
```

### 恢复状态 (RESOLVED)
```
✅ Prometheus 告警恢复
状态: 已恢复

告警名称: HighCPUUsage
恢复时间: 2025-09-03 14:45:00
```

## 🔧 高级配置

### 过滤规则详解

1. **优先级规则**：`exclude` > `include`
2. **匹配逻辑**：告警必须同时通过 `alert_name` 和 `severity` 两个维度
3. **通配符支持**：
   - `*`: 匹配任意字符
   - `HighCPU*`: 前缀匹配
   - `*Memory*`: 包含匹配
   - `*Test`: 后缀匹配

### 消息分批机制

企业微信存在4096字节消息长度限制，系统会自动：
- 检测消息长度，超长时自动分批
- 保持告警内容完整性，不截断信息
- 按序发送，避免消息混乱
- 添加发送间隔，防止频率限制

## 🧪 测试工具

### 传统告警过滤测试
项目提供了测试脚本来验证过滤功能：

```bash
# Linux/macOS
chmod +x test_filter_api.sh
./test_filter_api.sh

# Windows PowerShell
.\test_filter_api.ps1
```

### 大流量告警测试
测试大流量告警功能：

```bash
# Linux/macOS
chmod +x scripts/test_traffic_alert.sh
./scripts/test_traffic_alert.sh

# Windows PowerShell
.\scripts\test_traffic_alert.ps1
```

测试脚本将：
1. 检查 ClickHouse 连接
2. 验证数据库和表的存在
3. 插入模拟的大流量数据
4. 验证数据是否正确插入

## 📊 监控和日志

### 日志格式
```
2025/09/03 14:30:00 [wechat] 告警分为 2 批发送
2025/09/03 14:30:00 [wechat] 第 1 批消息发送成功
2025/09/03 14:30:00 告警 [HighCPUUsage] 级别 [critical] 通过过滤规则
2025/09/03 14:30:00 告警 [TestAlert] 级别 [warning] 被过滤规则拦截
```

### 日志轮转配置
- 文件位置：`./logs/alter-server.log`
- 单文件大小：1MB
- 保留文件数：5个
- 保留天数：7天
- 自动压缩：开启

## 🚀 部署建议

### Docker 部署
```dockerfile
FROM alpine:latest
RUN apk --no-cache add ca-certificates
WORKDIR /app
COPY alert-webhook .
COPY config.yaml .
EXPOSE 18082
CMD ["./alert-webhook"]
```

### Systemd 服务
```ini
[Unit]
Description=Alert Webhook Notifier
After=network.target

[Service]
Type=simple
User=alert-webhook
WorkingDirectory=/opt/alert-webhook
ExecStart=/opt/alert-webhook/alert-webhook
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
```

## 🔍 故障排除

### 常见问题

1. **配置验证失败**
   - 检查 YAML 格式是否正确
   - 确认所有启用的客户端都有对应的 notifier 配置

2. **Webhook 连接失败**
   - 验证 URL 格式和有效性
   - 检查网络连接和防火墙设置

3. **消息发送失败**
   - 查看详细错误日志
   - 验证 Webhook 密钥是否正确

4. **过滤规则不生效**
   - 检查配置文件中的过滤规则语法
   - 确认告警字段名称和值匹配规则

## 📝 更新日志

### v3.0.0 (2025-09-16)
- ✨ 新增基于 ClickHouse 的大流量告警功能
- ✨ 支持自定义请求/响应大小阈值和计数阈值
- ✨ 智能检测 Nginx 访问日志中的异常大流量
- ✨ 提供详细的流量统计和异常请求示例
- ✨ 支持按域名和路径分组的精准监控
- 🐛 修复配置文件错误处理问题
- 📚 完善文档和测试脚本

### v2.0.0 (2025-09-03)
- ✨ 新增智能告警过滤功能
- ✨ 支持企业微信长消息自动分批
- ✨ 优化多平台并发推送性能
- 🐛 修复配置文件循环依赖问题

### v1.0.0
- 🎉 初始版本发布
- 📢 支持企业微信、钉钉、飞书三大平台
- 🎨 美化告警消息格式

## 🤝 贡献指南

1. Fork 本项目
2. 创建特性分支 (`git checkout -b feature/amazing-feature`)
3. 提交更改 (`git commit -m 'Add some amazing feature'`)
4. 推送到分支 (`git push origin feature/amazing-feature`)
5. 创建 Pull Request

## 📄 开源协议

本项目基于 [MIT License](LICENSE) 开源协议。

## 🙏 致谢

感谢所有贡献者和使用者对本项目的支持！

---

<p align="center">
  如果这个项目对你有帮助，请给个 ⭐ Star 支持一下！
</p>