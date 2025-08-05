package main

import (
	"alert-webhook/config"
	"alert-webhook/console"
	"github.com/natefinch/lumberjack"
	"log"
)

func main() {
	// 测试所有客户端连通性
	allSuccess := config.TestClientsConnection()

	if allSuccess {
		console.Success("[Success]", "所有客户端连通性测试成功")
	} else {
		console.Error("[Failed]", "部分客户端连通性测试失败，将继续启动服务")
	}

	console.Success("服务已启动在: ", config.ServerPort)
	config.StartWebhookServer(config.ServerPort)
}

// init 设置日志轮转配置
func init() {
	log.SetOutput(&lumberjack.Logger{
		Filename:   "./logs/alter-server.log",
		MaxSize:    1,
		MaxBackups: 5,
		MaxAge:     7,
		Compress:   true,
	})
	log.SetFlags(log.LstdFlags | log.Lshortfile)
}
