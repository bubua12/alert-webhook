#!/bin/bash

APP_NAME="kube-alert"
LOG_DIR="./logs"
LOG_FILE="$LOG_DIR/server.log"
PID_FILE="wecom-alert.pid"

mkdir -p $LOG_DIR

# 检查是否已经运行
if [ -f "$PID_FILE" ] && kill -0 $(cat "$PID_FILE") 2>/dev/null; then
  echo "$APP_NAME 已在运行 (PID=$(cat $PID_FILE))"
  exit 1
fi

# 启动服务
echo "启动 $APP_NAME ..."
nohup ./$APP_NAME > "$LOG_FILE" 2>&1 &

echo $! > "$PID_FILE"
echo "$APP_NAME 已启动，日志记录于 $LOG_FILE"

