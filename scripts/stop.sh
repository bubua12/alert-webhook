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

PID_FILE="$APP_HOME/$APP_NAME.pid"
if [ ! -f "$PID_FILE" ]; then
  echo "PID 文件不存在，程序可能未运行"
  exit 0
fi

PID="$(tr -d '[:space:]' < "$PID_FILE" || true)"
if [ -z "$PID" ]; then
  echo "PID 文件为空，已清理"
  rm -f "$PID_FILE"
  exit 0
fi

if ! kill -0 "$PID" 2>/dev/null; then
  echo "进程 $PID 不存在，已清理过期 PID 文件"
  rm -f "$PID_FILE"
  exit 0
fi

echo "正在停止 $APP_NAME (PID=$PID) ..."
kill "$PID"

# Wait up to 10s (20 * 0.5s) to cover Go server's 5s graceful shutdown timeout.
for _ in $(seq 1 20); do
  if ! kill -0 "$PID" 2>/dev/null; then
    rm -f "$PID_FILE"
    echo "$APP_NAME 已优雅停止"
    exit 0
  fi
  sleep 0.5
done

echo "进程仍未退出，执行强制结束 (SIGKILL)..."
kill -9 "$PID" 2>/dev/null || true
rm -f "$PID_FILE"
echo "$APP_NAME 已强制停止"
