# TODO

记录待优化的运行维护与磁盘空间控制事项（按优先级排序）。

---

## P1 — 磁盘文件大小上限与自动清理

### 1. [ ] `data/alerts.jsonl` 自动压缩与容量控制

- **涉及文件与符号**：
  - `store/alert_store.go`：`defaultAlertStoreCapacity = 1000`、`AlertStore`、`AlertStore.Add`、`appendJSONL`、`loadFromFile`、`Clear`
  - `main.go`：`main`（`store.NewAlertStore` 初始化）
- **当前行为**：
  - `AlertStore` 在内存中以环形缓冲上限保存最近 `1000` 条告警记录；
  - 磁盘文件 `<data_dir>/alerts.jsonl` 在 `appendJSONL` 中通过 `os.O_CREATE|os.O_WRONLY|os.O_APPEND` 纯追加写入，**没有自动截断或压缩机制**（仅控制台触发 `Clear()` 时会清空）；
  - 长期运行或发生告警风暴时，`alerts.jsonl` 会持续增长，且下次重启执行 `loadFromFile()` 时需要顺序扫描整个历史文件。
- **拟实现方案**：
  1. 在 `AlertStore` 中跟踪累计追加行数 / 文件字节数；
  2. 当磁盘行数超过 `2 * capacity`（或超过固定大小阈值，如 `5 MB`）时，在锁保护下执行**原子重写压缩**：仅保留内存中最新的 `capacity` 条记录，先写入同目录临时文件并 `Sync()`，再通过 `os.Rename` 原子替换 `alerts.jsonl`；
  3. 启动阶段 `loadFromFile()` 若发现磁盘历史行数/体积已超过阈值，也自动触发一次压缩清理；
  4. 可选提供配置项或环境变量（如最大保留条数、单文件上限），默认保持开箱即用安全上限。
- **验收标准**：
  - 在 `store/alert_store_test.go` 补充单元测试并在 `go test -race -count=1 ./...` 下通过；
  - 验证连续写入超过压缩阈值后，`alerts.jsonl` 行数与体积稳定收敛在可控上限内，重启重新加载记录完整不丢失、写入过程异常安全。

---

### 2. [ ] `logs/server.log`（`./start.sh`）与 Docker 容器日志轮转限制

- **涉及文件与符号**：
  - `scripts/start.sh`：`nohup "$BIN_PATH" "$@" >> "$LOG_FILE" 2>&1 &`（写入 `$LOG_DIR/server.log`）
  - `main.go`：`setupLogging`（将日志同时写入 `os.Stdout`、`lumberjack.Logger` 管理的 `alter-server.log` 与内存 `LogBuffer`）
  - `docker-compose.yml`：`services.alert-webhook`
- **当前行为**：
  - `logs/alter-server.log` 已通过 `lumberjack.Logger`（`MaxSize: 1` MB、`MaxBackups: 5`、`MaxAge: 7` 天、`Compress: true`）受控轮转；
  - 但直接以 `./start.sh` 后台启动时，`nohup ... >> logs/server.log 2>&1` 会把同步输出的 `os.Stdout` 完整日志额外再写一份到 `logs/server.log`，且 `server.log` **没有大小上限与轮转清理机制**；
  - `docker-compose.yml` 目前未显式声明容器 `logging` 配额（依赖宿主机 Docker daemon 默认配置）。
- **拟实现方案**：
  1. 当通过 `./start.sh` 后台守护模式启动时（例如注入 `ALERT_WEBHOOK_DAEMON=1`），在 `setupLogging` 完成 `lumberjack` 文件日志初始化后停止向 `os.Stdout` 重复输出常规日志（仅保留初始化前异常、panic 与 `stderr` 错误输出），并在 `./start.sh` 启动时检查若已有 `server.log` 过大则执行轮转/截断；
  2. 在 `docker-compose.yml` 为 `alert-webhook` 增加 `logging` 限制（如 `driver: json-file`，`max-size: "10m"`、`max-file: "3"`）；
  3. 视需要在 `README.md` 补充宿主机 `logrotate` 示例配置，兼容直接后台运行场景。
- **验收标准**：
  - 使用 `./start.sh` 启动后持续产生日志，`logs/` 目录总占用保持在固定上限以内；
  - Web 控制台「运行日志」实时缓冲与 `logs/alter-server.log` 正常记录均不受影响。
