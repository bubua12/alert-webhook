package main

import (
	"alert-webhook/config"
	"fmt"
	"github.com/natefinch/lumberjack"
	"log"
)

func main() {
	connectSuccess := config.TestWecomConnection()
	if connectSuccess {
		fmt.Println("✅ 企业微信 Webhook 服务已启动在: ", config.WebhookServerPort)
		config.StartWebhookServer(config.WebhookServerPort)
	} else {
		fmt.Println("❌ 企业微信 Webhook 不可用，跳过 webhook 服务启动")
	}
}

func init() {
	log.SetOutput(&lumberjack.Logger{
		Filename:   "./logs/alter-server.log", // 日志文件路径
		MaxSize:    1,                         // 每个文件最大 10MB
		MaxBackups: 5,                         // 最多保留 5 个旧文件
		MaxAge:     7,                         // 最多保留 7 天
		Compress:   true,                      // 启用压缩
	})
	log.SetFlags(log.LstdFlags | log.Lshortfile) // 输出时间和文件行号
}
