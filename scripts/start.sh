#!/usr/bin/env bash
set -euo pipefail

APP_NAME="alert-webhook"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if [ -f "$SCRIPT_DIR/$APP_NAME" ]; then
  APP_HOME="$SCRIPT_DIR"
elif [ -f "$SCRIPT_DIR/../$APP_NAME" ] || [ -f "$SCRIPT_DIR/../go.mod" ]; then
  APP_HOME="$(cd "$SCRIPT_DIR/.." && pwd)"
else
  APP_HOME="$SCRIPT_DIR"
fi

cd "$APP_HOME"

trim_ws() {
  local s="$1"
  s="${s#"${s%%[![:space:]]*}"}"
  s="${s%"${s##*[![:space:]]}"}"
  printf '%s' "$s"
}

load_dotenv_file() {
  local env_file="$1"
  [ -f "$env_file" ] || return 0

  while IFS= read -r raw_line || [ -n "$raw_line" ]; do
    raw_line="${raw_line%$'\r'}"
    raw_line="${raw_line#$'\xef\xbb\xbf'}"
    local line
    line="$(trim_ws "$raw_line")"
    [ -z "$line" ] && continue
    [[ "$line" == \#* ]] && continue

    if [[ "$line" == export[[:space:]]* ]]; then
      line="$(trim_ws "${line#export}")"
    fi
    [[ "$line" == *=* ]] || continue

    local key val
    key="$(trim_ws "${line%%=*}")"
    val="${line#*=}"
    [[ "$key" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || continue

    val="${val#"${val%%[![:space:]]*}"}"
    if [[ "$val" == \'*\' && ${#val} -ge 2 ]]; then
      val="${val:1:${#val}-2}"
    elif [[ "$val" == \"*\" && ${#val} -ge 2 ]]; then
      val="${val:1:${#val}-2}"
      val="${val//\\n/$'\n'}"
      val="${val//\\r/$'\r'}"
      val="${val//\\t/$'\t'}"
      val="${val//\\\"/\"}"
      val="${val//\\\\/\\}"
    else
      if [[ "$val" =~ ^(.*[^[:space:]])[[:space:]]+#.*$ ]]; then
        val="${BASH_REMATCH[1]}"
      fi
      val="$(trim_ws "$val")"
    fi

    # Do not override variables already exported in the current shell.
    if [ -z "${!key+x}" ]; then
      export "$key=$val"
    fi
  done < "$env_file"
}

ENV_FILE="${ALERT_WEBHOOK_ENV_FILE:-$APP_HOME/.env}"
ENV_LOADED_MSG="未检测到 .env（将使用配置文件或默认配置）"
if [ -f "$ENV_FILE" ]; then
  load_dotenv_file "$ENV_FILE"
  ENV_LOADED_MSG="$ENV_FILE"
fi

BIN_PATH="$APP_HOME/$APP_NAME"
if [ ! -f "$BIN_PATH" ]; then
  echo "[Error] 未找到可执行文件: $BIN_PATH" >&2
  echo "请先执行 make build 或 make package 构建发布包。" >&2
  exit 1
fi
if [ ! -x "$BIN_PATH" ]; then
  chmod +x "$BIN_PATH"
fi

PID_FILE="$APP_HOME/$APP_NAME.pid"
if [ -f "$PID_FILE" ]; then
  OLD_PID="$(tr -d '[:space:]' < "$PID_FILE" || true)"
  if [ -n "$OLD_PID" ] && kill -0 "$OLD_PID" 2>/dev/null; then
    echo "$APP_NAME 已在运行 (PID=$OLD_PID)"
    exit 1
  fi
  rm -f "$PID_FILE"
fi

LOG_DIR="${ALERT_WEBHOOK_LOG_DIR:-$APP_HOME/logs}"
mkdir -p "$LOG_DIR"
LOG_FILE="$LOG_DIR/server.log"

echo "正在启动 $APP_NAME ..."
nohup "$BIN_PATH" "$@" >> "$LOG_FILE" 2>&1 &
NEW_PID=$!
echo "$NEW_PID" > "$PID_FILE"

# Wait briefly to verify the process did not crash immediately (e.g. port in use or bad YAML).
ALIVE=1
for _ in 1 2 3 4 5 6; do
  if ! kill -0 "$NEW_PID" 2>/dev/null; then
    ALIVE=0
    break
  fi
  sleep 0.5
done

if [ "$ALIVE" -ne 1 ]; then
  echo "[Error] $APP_NAME 启动失败，进程已提前退出。最近日志:" >&2
  tail -n 20 "$LOG_FILE" >&2 || true
  rm -f "$PID_FILE"
  exit 1
fi

EFFECTIVE_PORT="${ALERT_WEBHOOK_PORT:-0.0.0.0:18082}"
EFFECTIVE_CFG="${ALERT_WEBHOOK_CONFIG:-$APP_HOME/config.yaml}"
EFFECTIVE_KEY="${API_KEY:-${ALERT_WEBHOOK_API_KEY:-${ALERT_WEBHOOK_AUTH_TOKEN:-}}}"
if [ -n "$EFFECTIVE_KEY" ]; then
  AUTH_MSG="已开启 (通过 API_KEY 登录)"
else
  AUTH_MSG="未开启（可在 .env 中设置 API_KEY 启用登录页）"
fi

echo "$APP_NAME 启动成功!"
echo "  PID      : $NEW_PID"
echo "  监听地址 : $EFFECTIVE_PORT"
echo "  配置文件 : $EFFECTIVE_CFG"
echo "  环境变量 : $ENV_LOADED_MSG"
echo "  登录鉴权 : $AUTH_MSG"
echo "  日志文件 : $LOG_FILE"
echo "  停止命令 : $SCRIPT_DIR/stop.sh"
