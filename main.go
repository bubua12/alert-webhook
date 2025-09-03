package main

import (
	"alert-webhook/config"
	"alert-webhook/console"
	"alert-webhook/service"
	"errors"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/natefinch/lumberjack"
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
	startWebhookServer(config.ServerPort)
}

// startWebhookServer 启动webhook服务
func startWebhookServer(addr string) {
	router := gin.New()
	router.POST("/webhook-alert", service.GinAlertHandler(config.Notifiers, config.EnabledClients, config.GlobalConfig))

	srv := &http.Server{
		Addr:    addr,
		Handler: router,
	}

	log.Printf("Webhook Gin 服务启动于 %s\n", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("Webhook Gin 服务启动失败: %v", err)
	}
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
