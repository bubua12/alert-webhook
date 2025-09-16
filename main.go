package main

import (
	"alert-webhook/config"
	"alert-webhook/console"
	"alert-webhook/service"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

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

	// 初始化ClickHouse服务（如果启用）
	var clickhouseService *service.ClickHouseService
	var trafficAlertService *service.TrafficAlertService

	if config.GlobalConfig.TrafficAlert.Enabled {
		var err error
		clickhouseService, err = service.NewClickHouseService(config.GlobalConfig)
		if err != nil {
			log.Printf("ClickHouse服务初始化失败: %v", err)
			console.Error("[Warning]", "ClickHouse连接失败，大流量告警功能将被禁用")
		} else {
			// 测试ClickHouse连接
			if err := clickhouseService.TestConnection(); err != nil {
				log.Printf("ClickHouse连接测试失败: %v", err)
				console.Error("[Warning]", "ClickHouse连接测试失败，大流量告警功能将被禁用")
				err := clickhouseService.Close()
				if err != nil {
					return
				}
				clickhouseService = nil
			} else {
				// 启动流量告警服务
				trafficAlertService = service.NewTrafficAlertService(
					clickhouseService,
					config.GlobalConfig,
					config.Notifiers,
					config.EnabledClients,
				)
				trafficAlertService.Start()
				console.Success("[Success]", "大流量告警服务启动成功")
			}
		}
	} else {
		log.Println("大流量告警功能未启用")
	}

	console.Success("服务已启动在: ", config.ServerPort)

	// 设置优雅关闭
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Println("收到关闭信号，正在优雅关闭...")

		// 停止流量告警服务
		if trafficAlertService != nil {
			trafficAlertService.Stop()
		}

		// 关闭ClickHouse连接
		if clickhouseService != nil {
			err := clickhouseService.Close()
			if err != nil {
				return
			}
		}

		os.Exit(0)
	}()

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
