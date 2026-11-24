<h1 align="center">🔔 Alertmanager Webhook Notifier</h1>
<p align="center">将 Prometheus Alertmanager、ClickHouse 流量异常与 Kafka 消费积压告警推送至企业微信 / 钉钉 / 飞书，并内置可视化 Web 运维控制台</p>
<p align="center">
  <img src="https://img.shields.io/badge/Version-v1.2.0-brightgreen.svg" alt="Version">
  <img src="https://img.shields.io/badge/Go-1.24-blue.svg" alt="Go Version">
  <img src="https://img.shields.io/badge/Platform-Linux%20%7C%20Windows%20%7C%20macOS%20%7C%20Docker-lightgrey.svg" alt="Platform">
  <img src="https://img.shields.io/badge/License-MIT-green.svg" alt="License">
  <img src="https://img.shields.io/github/downloads/bubua12/alert-webhook/total.svg" alt="Releases">
</p>

---

## ✨ 项目简介 & v1.2.0 亮点

本项目是一个轻量高性能的告警转发与监控服务，基于 **Go 1.24 + Gin** 构建，统一接收 Prometheus Alertmanager Webhook，并内置基于 ClickHouse 的 Nginx 大流量异常检测与基于 Kafka UI 的消费者组积压监控。

### 🆕 v1.2.0 核心升级

- 🖥️ **内置可视化 Web 控制台**（零外部 CDN 依赖，纯离线可用，支持浅色 / 深色主题）：
  - **监控总览**：24 小时告警时间线、严重级别分布、通道投递健康度、ClickHouse/Kafka 服务状态与一键触发检测。
  - **告警记录审计**：支持按告警状态、投递结果、告警来源、级别与关键词分页查询，点击行展开详情抽屉（Labels、Annotations、过滤原因及各渠道响应耗时），支持导出 JSON 与清空记录。
  - **可视化配置中心与热更新**：企业微信 / 钉钉 / 飞书卡片式配置与在线连通性测试；通配符过滤规则编辑器与实时命中测试；ClickHouse / Kafka UI 连接探测与阈值可视化配置；YAML 原生编辑与导入导出，保存后原子落盘并立即热生效。
  - **调试沙箱**：内置常见告警场景模板，支持 **预览渲染（Dry Run）** 查看三平台消息效果，或 **真实发送测试** 联调推送链路。
  - **实时运行日志**：按 `INFO` / `WARN` / `ERROR` 级别过滤与关键词检索，实时查看服务运行日志。
- 🚀 **零配置友好启动与环境变量覆盖**：即使缺少或留空 `config.yaml`（甚至未配置任何通知渠道），服务依然可正常启动并在控制台引导完成初始化；支持通过 `ALERT_WEBHOOK_*` 环境变量灵活注入或覆盖配置。
- 🐳 **开箱即用的 Docker / Docker Compose 部署**：提供多阶段 Alpine 非 root 容器镜像，内置 `tzdata`（默认 `Asia/Shanghai`）、`/healthz` 健康检查与标准输出日志（适配 `docker logs`）。

---

## 🌟 核心能力一览

| 模块 | 能力说明 |
| :--- | :--- |
| **多平台并发推送** | 支持企业微信（Markdown + 超长 4096 字节自动分批）、钉钉（Markdown）、飞书（文本），校验平台业务返回码（`errcode`/`code`） |
| **智能告警过滤** | 支持 `alert_name` 与 `severity` 双维度 `include` / `exclude` 与 `*` 通配符（`exclude` 优先） |
| **Nginx 大流量监控** | 定时查询 ClickHouse `nginx_access` 日志，按域名与路径聚合检测超大请求 / 超大响应异常 |
| **Kafka 消费积压监控** | 通过 Kafka UI API 定时巡检消费组 Lag，支持指定监控名单或忽略名单 |
| **可视化运维台** | 单二进制内嵌前端静态资源（`embed.FS`），同一端口（默认 `18082`）提供告警接收与 Web 控制台 |

---

## 📦 快速开始

### 方式一：Docker Compose 一键部署（推荐）

```bash
# 1. 克隆仓库
git clone https://github.com/bubua12/alert-webhook.git
cd alert-webhook

# 2. （可选）准备环境变量文件（端口、API Token、Webhook URL 等）
cp .env.example .env

# 3. （可选）准备初始配置文件；若省略此步，首次启动会自动在 ./conf/config.yaml 生成默认配置
mkdir -p conf data logs
cp config-example.yaml conf/config.yaml

# 4. 启动服务（自动创建宿主机目录、构建镜像并后台启动）
make docker-up
# 或直接执行：
# mkdir -p conf data logs && docker compose up -d --build

# 5. 查看运行日志与健康状态
make docker-logs
curl http://localhost:18082/healthz
```

启动成功后，浏览器访问 `http://localhost:18082` 即可进入 Web 控制台完成通知渠道配置与联调测试。

> **💡 为什么挂载 `./conf:/app/config` 目录（而不是单文件 `./config.yaml` 或 `./config`）？**
> 1. 仓库根目录下的 `./config/` 是 Go 源码包目录（存放 `config/*.go`），宿主机使用 `./conf` 目录可彻底避免与源码目录混淆；
> 2. 若宿主机尚不存在 `config.yaml` 而直接挂载单文件，Docker 会自动创建一个同名空目录，导致程序读取文件失败；
> 3. 挂载目录后，容器内 Web 控制台在执行配置热更新时可通过「写临时文件 + 原子 `rename`」安全保存，避免单文件挂载触发 `EBUSY` 错误。

> **🐧 Linux 宿主机权限提示**：容器默认以非 root 用户 `appuser`（UID `100` / GID `101`）运行。若在 Linux 宿主机遇到 `./conf`、 `./data` 或 `./logs` 写入权限错误，可执行 `chown -R 100:101 conf data logs` 后重启容器。

### 方式二：Docker CLI 运行

```bash
# 1. 构建镜像
docker build -t alert-webhook:latest .

# 2. 准备持久化目录
mkdir -p conf data logs

# 3. 运行容器（可通过 -e 直接注入通知渠道，无需提前准备 config.yaml）
docker run -d \
  --name alert-webhook \
  --restart unless-stopped \
  -p 18082:18082 \
  -e TZ=Asia/Shanghai \
  -e ALERT_WEBHOOK_WECHAT_URL="https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=xxxxxxxx" \
  -v "$(pwd)/conf:/app/config" \
  -v "$(pwd)/data:/app/data" \
  -v "$(pwd)/logs:/app/logs" \
  alert-webhook:latest
```

### 方式三：源码 / 二进制启动

要求 **Go 1.24+**。

```bash
# 1. 编译二进制（自动内嵌 web/static 前端资源）
make build
# 或交叉编译 Linux amd64 版本：
# CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o alert-webhook .

# 2. 准备配置（可选，省略时启动自动生成默认 ./config.yaml）
cp config-example.yaml config.yaml

# 3. 启动服务
./alert-webhook
# 或指定配置文件路径：
# ./alert-webhook -config=/path/to/config.yaml

# 开发模式直接运行与测试
make run
make test
make test-race
```

如习惯传统后台脚本启停，也可继续使用项目自带脚本：

```bash
chmod +x scripts/start.sh scripts/stop.sh
./scripts/start.sh
./scripts/stop.sh
```

---

## 🖥️ Web 控制台使用说明

服务启动后，在浏览器打开 **`http://localhost:18082`**（如需关闭 Web 界面，可在配置中设置 `server.ui_enabled: false`）：

1. **安全认证（可选）**：若设置了 `server.auth_token` 或环境变量 `ALERT_WEBHOOK_AUTH_TOKEN`，控制台会在首次访问 `/api/*` 时弹出 Token 解锁弹窗，验证后保存在浏览器 `localStorage`。`/webhook-alert`、`/healthz`、`/readyz` 与静态资源不受 Token 限制，确保 Alertmanager 与探针正常接入。
2. **监控总览**：查看累计告警、推送成功率、24 小时趋势图、通道平均延迟，并可查看与手动触发 ClickHouse 大流量检测或 Kafka 积压检测。
3. **告警记录**：筛选排查所有收到的告警（包括被过滤规则拦截的记录），展开详情查看每条消息的标签、注释与各平台投递明细。
4. **配置中心**：
   - 在「通知渠道」填写 Webhook URL 后可直接点击 **测试连通性** 验证再保存；
   - 在「过滤规则」通过标签选择器编辑 `include` / `exclude` 并使用内嵌规则测试器验证；
   - 在「服务与 YAML」支持可视化切换原始 YAML、导入/导出配置或一键生成 `alertmanager.yml` 片段；
   - 修改 `server.port` 需重启进程生效，其余通知渠道、过滤规则、ClickHouse、Kafka 监控均支持 **保存并热生效**。
5. **调试沙箱**：选择预设告警模板，支持先 **Dry Run 预览** 三平台文案，再 **真实发送测试**。
6. **运行日志**：在线查看最近 500 条运行日志，支持按级别和关键词筛选。

---

## 🔗 Alertmanager 集成配置

在 Prometheus Alertmanager 的 `alertmanager.yml` 中添加 Webhook 接收器：

```yaml
route:
  group_by: ['alertname']
  group_wait: 10s
  group_interval: 10s
  repeat_interval: 1h
  receiver: 'webhook-notifier'

receivers:
  - name: 'webhook-notifier'
    webhook_configs:
      # 宿主机或跨机器访问：
      - url: 'http://your-server-ip:18082/webhook-alert'
      # 若与 Alertmanager 同处一个 Docker Compose 网络，可使用服务名：
      # - url: 'http://alert-webhook:18082/webhook-alert'
        send_resolved: true
```

---

## ⚙️ 配置文件与环境变量一览

完整配置模板请参考 [config-example.yaml](config-example.yaml)（Kafka 专项示例见 [config-kafka-example.yaml](config-kafka-example.yaml)），环境变量模板见 [.env.example](.env.example)。

### 环境变量一览表

> 当设置了下表中非空环境变量时，将覆盖配置文件中的对应字段；在 Web 控制台「配置中心」顶部会展示当前生效的环境变量覆盖项提示。其中设置 `ALERT_WEBHOOK_*_URL` 时会自动填充对应 `notifiers.<client>.webhook_url` 并确保该渠道处于启用状态。

| 环境变量 | 默认值 / 容器默认值 | 说明 |
| :--- | :--- | :--- |
| `ALERT_WEBHOOK_HOST_PORT` | `18082` | 仅 Docker Compose 使用：映射到容器 `18082` 端口的宿主机端口 |
| `ALERT_WEBHOOK_CONFIG` | `./config.yaml`（容器 `/app/config/config.yaml`） | 配置文件路径（优先级低于命令行 `-config` 参数） |
| `ALERT_WEBHOOK_PORT` | `0.0.0.0:18082` | HTTP 服务监听地址 |
| `ALERT_WEBHOOK_AUTH_TOKEN` | `""` | `/api/*` 管理接口鉴权 Token（支持 `Bearer` 或 `X-API-Token`） |
| `ALERT_WEBHOOK_DATA_DIR` | `./data`（容器 `/app/data`） | 告警审计历史文件 `alerts.jsonl` 存储目录 |
| `ALERT_WEBHOOK_LOG_DIR` | `./logs`（容器 `/app/logs`） | 滚动日志文件目录（日志同时输出至 `stdout`） |
| `ALERT_WEBHOOK_WECHAT_URL` | `""` | 企业微信群机器人 Webhook URL |
| `ALERT_WEBHOOK_DINGTALK_URL` | `""` | 钉钉群机器人 Webhook URL |
| `ALERT_WEBHOOK_FEISHU_URL` | `""` | 飞书群机器人 Webhook URL |
| `ALERT_WEBHOOK_CLICKHOUSE_HOST` | `""` | ClickHouse 主机地址 |
| `ALERT_WEBHOOK_CLICKHOUSE_PORT` | `""` | ClickHouse Native 端口（通常为 `9000`） |
| `ALERT_WEBHOOK_CLICKHOUSE_DB` | `""` | ClickHouse 数据库名（默认 `nginxlogs`） |
| `ALERT_WEBHOOK_CLICKHOUSE_USER` | `""` | ClickHouse 用户名 |
| `ALERT_WEBHOOK_CLICKHOUSE_PASSWORD` | `""` | ClickHouse 密码 |
| `ALERT_WEBHOOK_TRAFFIC_ENABLED` | `""` | 是否启用大流量告警（`true` / `false`） |
| `ALERT_WEBHOOK_KAFKA_ENABLED` | `""` | 是否启用 Kafka 积压监控（`true` / `false`） |
| `ALERT_WEBHOOK_KAFKA_UI_URL` | `""` | Kafka UI 访问地址（不含路径，如 `http://localhost:8090`） |
| `ALERT_WEBHOOK_KAFKA_CLUSTER` | `""` | Kafka UI 中配置的集群名称 |
| `TZ` | `Asia/Shanghai` | 容器与时间格式化时区 |

---

## 📊 ClickHouse 与 Kafka UI 监控配置

### 1. ClickHouse 大流量告警准备（可选）

如果启用 `traffic_alert.enabled: true`，需要在 ClickHouse 中提前准备 `nginx_access` 访问日志表（可在 `clickhouse.database` 中自定义库名，仅允许字母、数字和下划线）：

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

配置好后，可在 Web 控制台「配置中心 → 流量监控」点击 **测试 ClickHouse 连接**，验证连通性与最近 1 分钟日志行数。

### 2. Kafka UI 消费组监控（可选）

启用 `kafka_monitor.enabled: true` 后，服务会定期调用 Kafka UI API 查询指定 `cluster_name` 的消费者组积压情况：
- `monitored_groups`：为空时监控所有消费者组；配置列表时仅监控指定组。
- `ignored_groups`：优先级高于 `monitored_groups`，用于排除无需关注的消费者组。
- 在 Web 控制台「配置中心 → Kafka 监控」点击 **测试 Kafka UI 连接**，可直观查看实时消费组与 Lag 列表，并一键加入监控或忽略名单。

### 3. 过滤规则与消息分批机制

- **过滤规则**：`exclude` 优先级高于 `include`；`alert_name` 和 `severity` 两个维度需同时通过。支持 `*` 通配符（如 `HighCPU*`、`*Memory*`、`*Test*`）。
- **企业微信自动分批**：针对企业微信 4096 字节单条消息限制，系统会自动拆分多条告警按序发送，确保信息完整不截断。

---

## 📋 API 接口文档

### 公开端点（无需认证）

| 方法与路径 | 说明 | 示例响应 |
| :--- | :--- | :--- |
| `POST /webhook-alert` | 接收 Prometheus Alertmanager Webhook 并推送到已启用平台，同步记录到历史审计 | `200` 成功 / `400` 参数错误 / `500` 推送失败 |
| `GET /healthz` | 存活探针（Liveness Probe） | `200 {"status":"ok","uptime_seconds":120,"version":"1.2.0"}` |
| `GET /readyz` | 就绪探针（Readiness Probe） | `200 {"status":"ready","config_loaded":true,"enabled_clients":["wechat"]}` |

### 管理控制台端点（`/api/*`）

> 若启用了 `auth_token`，所有 `/api/*` 请求需在 Header 中携带 `Authorization: Bearer <token>` 或 `X-API-Token: <token>`，未授权将返回 `401 {"error":"unauthorized","auth_required":true}`。

| 方法与路径 | 说明 |
| :--- | :--- |
| `GET /api/overview` | 获取服务版本、启动时间、统计指标、24 小时时间线、后台巡检状态及最近 8 条告警 |
| `GET /api/alerts` | 分页检索告警记录，支持 `page`、`page_size`、`status`、`delivery_state`、`source`、`severity`、`q` 参数 |
| `DELETE /api/alerts` | 清空告警历史记录 |
| `GET /api/config` | 获取当前生效配置 JSON、原始 YAML 文本、校验警告及环境变量覆盖列表 |
| `PUT /api/config` | 提交 JSON 格式配置，校验并原子保存后热重载服务与过滤规则 |
| `PUT /api/config/yaml` | 提交 `{ "yaml": "..." }` 原始文本并校验热重载 |
| `POST /api/config/reload` | 从磁盘重新加载配置文件并热更新 |
| `POST /api/test/notifier` | 测试通知渠道连通性（支持传入 `{ client, webhook_url }` 临时测试未保存的 URL） |
| `POST /api/test/clickhouse` | 测试 ClickHouse 连通性并返回最近 1 分钟日志数 |
| `POST /api/test/kafka` | 测试 Kafka UI 连接并返回发现的消费者组列表 |
| `POST /api/filter/test` | 使用指定过滤规则测试 `alert_name` 与 `severity` 是否放行 |
| `POST /api/simulate` | 告警调试沙箱（`send: false` 仅返回三平台预览文案；`send: true` 真实推送并记录） |
| `POST /api/services/traffic/check` | 立即触发一次 ClickHouse 大流量检测 |
| `POST /api/services/kafka/check` | 立即触发一次 Kafka 消费者组积压检测 |
| `GET /api/logs` | 查询最近运行日志，支持 `level`（`INFO`/`WARN`/`ERROR`）、`q`、`limit` 参数 |

---

## 🎨 消息效果预览

### 大流量告警消息效果
```text
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

### Kafka 消费者组积压告警消息效果
```text
📊 Prometheus 告警通知
请关注告警信息，相关人员请注意

状态: 告警中
告警名称: Kafka消费者组积压告警
级别: P2
消费者组: order-group-01
摘要: Kafka消费者组 order-group-01 积压告警
描述: 消费者组积压信息:
• 消费者组ID: order-group-01
• 当前积压量: 299390
• 告警阈值: 100000
• 成员数量: 0
• 关联主题数: 1
• 组状态: EMPTY
• 协调器: 172.25.101.24:9092
触发时间: 2025-12-05 14:30:00
```

### 恢复状态 (RESOLVED)
```text
✅ Prometheus 告警恢复
状态: 已恢复

告警名称: HighCPUUsage
恢复时间: 2025-09-03 14:45:00
```

---

## 📊 日志、持久化与 Systemd 部署

### 日志与持久化路径

- **标准输出**：所有运行日志同时输出至 `stdout`，容器部署时可直接使用 `docker logs -f alert-webhook` 查看。
- **文件轮转**：日志同时写入 `<log_dir>/alter-server.log`（默认 `./logs/alter-server.log`），单文件 1MB、保留 5 份、保留 7 天、开启 gzip 压缩。
- **告警审计存储**：持久化在 `<data_dir>/alerts.jsonl`（默认 `./data/alerts.jsonl`），内存保留最近 1000 条记录。

### Systemd 服务示例

```ini
[Unit]
Description=Alert Webhook Notifier
After=network.target

[Service]
Type=simple
User=alert-webhook
WorkingDirectory=/opt/alert-webhook
ExecStart=/opt/alert-webhook/alert-webhook -config=/opt/alert-webhook/config.yaml
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
```

---

## 🧪 测试与排障

### 快速验证与联调

```bash
# 1. 运行单元测试与竞态检测
make test-race

# 2. 检查服务存活与就绪状态
curl http://localhost:18082/healthz
curl http://localhost:18082/readyz

# 3. 发送模拟 Alertmanager Webhook（或直接在 Web 控制台「调试沙箱」测试）
curl -X POST http://localhost:18082/webhook-alert \
  -H "Content-Type: application/json" \
  -d '{
    "status": "firing",
    "alerts": [{
      "status": "firing",
      "labels": {"alertname": "HighCPUUsage", "severity": "critical", "instance": "10.0.0.1:9100"},
      "annotations": {"summary": "CPU 使用率过高", "description": "节点 10.0.0.1 CPU 持续超过 90%"}
    }]
  }'
```

### 常见问题

1. **为什么未配置任何渠道也启动成功了？**
   - v1.2.0 起支持零配置启动，服务会在日志输出提示信息，并允许你直接打开 `http://localhost:18082` 在 Web 控制台完成后续配置。
2. **Webhook 推送日志显示 HTTP 200 但平台未收到消息？**
   - 企业微信、钉钉和飞书部分错误会在 HTTP 200 响应体中返回非 0 的 `errcode`/`code`。v1.2.0 已自动校验并记录完整错误响应，可在控制台「告警记录」详情或「运行日志」中直接查看原因。
3. **过滤规则不生效或配置保存后端口未变？**
   - 过滤规则中 `exclude` 优先级高于 `include`；可在控制台「配置中心 → 过滤规则」中使用在线测试工具校验。
   - 通知渠道、过滤规则与巡检服务支持保存即热生效，仅修改 `server.port` 需要重启进程或容器。

---

## 📝 更新日志

### v1.2.0
- ✨ 新增内置可视化 Web 控制台（监控总览、告警记录审计、可视化配置中心与热更新、调试沙箱、实时运行日志），支持浅色/深色主题与可选 API Token 认证
- ✨ 新增 `/healthz`、`/readyz` 健康探针与完整 `/api/*` REST 接口
- ✨ 支持零配置友好启动、原子配置持久化与 `ALERT_WEBHOOK_*` 全量环境变量覆盖
- ✨ 新增多阶段非 root `Dockerfile`、`docker-compose.yml`、`.env.example` 与 `Makefile` 一键构建部署
- 🐛 修复 `config` 包 `init()` 内 `flag.Parse()` 与写文件副作用导致的 `go test` 异常
- 🐛 修复并发推送告警时的切片数据竞争，新增 HTTP 客户端 10 秒超时及企业微信/钉钉/飞书 JSON `errcode`/`code` 校验
- 🐛 修复无系统 `tzdata` 环境下 `Asia/Shanghai` 加载隐患，内嵌 `time/tzdata` 并为飞书消息补全上海时区转换
- 🐛 修复 ClickHouse 查询硬编码数据库名问题及 `defer rows.Close()` 潜在致命退出问题
- 🐛 修复日志仅写入文件导致 `docker logs` 不可见的问题，改为 `stdout + 文件 + 内存缓冲` 多路输出并支持 HTTP 服务优雅停机

### v1.1.0 (2025-12-05)
- ✨ 新增 Kafka 消费者组监控功能
- ✨ 支持通过 Kafka UI API 实时获取消费者组信息
- ✨ 支持自定义消费者组积压阈值
- ✨ 支持监控指定消费者组或全部消费者组，以及忽略特定消费者组
- 📚 完善 Kafka 监控相关文档

### v1.0.1 (2025-09-16)
- ✨ 新增基于 ClickHouse 的大流量告警功能
- ✨ 支持自定义请求/响应大小阈值和计数阈值
- ✨ 智能检测 Nginx 访问日志中的异常大流量
- ✨ 提供详细的流量统计和异常请求示例
- ✨ 支持按域名和路径分组的精准监控
- 🐛 修复配置文件错误处理问题
- 📚 完善文档和测试脚本

### v1.0.0-bugfix (2025-09-03)
- ✨ 新增智能告警过滤功能
- ✨ 支持企业微信长消息自动分批
- ✨ 优化多平台并发推送性能
- 🐛 修复配置文件循环依赖问题

### v1.0.0
- 🎉 初始版本发布
- 📢 支持企业微信、钉钉、飞书三大平台
- 🎨 美化告警消息格式

---

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

<p align="center">
  如果这个项目对你有帮助，请给个 ⭐ Star 支持一下！
</p>
