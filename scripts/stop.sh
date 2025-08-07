#!/bin/bash

PID_FILE="wecom-alert.pid"

if [ ! -f "$PID_FILE" ]; then
  echo "PID 文件不存在，程序可能未运行"
  exit 1
fi

PID=$(cat "$PID_FILE")

if kill -0 $PID 2>/dev/null; then
  echo "停止进程 $PID ..."
  kill $PID
  sleep 2
  if kill -0 $PID 2>/dev/null; then
    echo "进程仍在运行，强制结束..."
    kill -9 $PID
  fi
  echo "停止完成"
  rm -f "$PID_FILE"
else
  echo "进程 $PID 不存在"
  rm -f "$PID_FILE"
fi

