package service

import (
	"alert-webhook/config"
	"errors"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
)

// ServerManager HTTP服务器管理器
type ServerManager struct {
	server *http.Server
}

// NewServerManager 创建服务器管理器
func NewServerManager() *ServerManager {
	return &ServerManager{}
}

// StartWebhookServer 启动webhook服务器
func (sm *ServerManager) StartWebhookServer(addr string) error {
	router := gin.New()
	router.POST("/webhook-alert", GinAlertHandler(config.Notifiers, config.EnabledClients, config.GlobalConfig))

	sm.server = &http.Server{
		Addr:    addr,
		Handler: router,
	}

	log.Printf("Webhook Gin 服务启动于 %s\n", addr)
	if err := sm.server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Shutdown 优雅关闭服务器
func (sm *ServerManager) Shutdown() error {
	if sm.server != nil {
		log.Println("正在关闭HTTP服务器...")
		return sm.server.Close()
	}
	return nil
}