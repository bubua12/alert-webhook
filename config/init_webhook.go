package config

import (
	"alert-webhook/service"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

var (
	EnabledClients []string
	Notifiers      map[string]string // client -> webhook_url
	ServerPort     string
)

func init() {
	// 加载配置
	configPath := flag.String("config", "./config.yaml", "配置文件路径")
	flag.Parse()

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("配置初始化失败: %v", err)
	}
	fmt.Println("配置初始化成功")

	EnabledClients = cfg.Clients
	ServerPort = cfg.Server.Port

	// 构建通知器映射
	Notifiers = make(map[string]string)
	for client, config := range cfg.Notifiers {
		Notifiers[client] = config.WebhookURL
	}
}

// TestClientsConnection 测试所有启用的客户端连通性
func TestClientsConnection() bool {
	allSuccess := true

	for _, client := range EnabledClients {
		url, ok := Notifiers[client]
		if !ok {
			log.Printf("客户端 %s 未配置", client)
			continue
		}

		var msg map[string]interface{}

		switch client {
		case "wechat":
			msg = map[string]interface{}{
				"msgtype": "markdown",
				"markdown": map[string]string{
					"content": "[测试连接]企业微信",
				},
			}
		case "dingtalk":
			msg = map[string]interface{}{
				"msgtype": "markdown",
				"markdown": map[string]string{
					"title": "钉钉连通性测试",
					"text":  "钉钉连通性测试",
				},
			}
		case "feishu":
			msg = map[string]interface{}{
				"msg_type": "text",
				"content": map[string]string{
					"text": "飞书连通性测试",
				},
			}
		default:
			log.Printf("未知客户端类型: %s", client)
			continue
		}

		if success := sendTestMessage(client, url, msg); success {
			log.Printf("[Connection Test Success] %s 连通性测试成功", client)
		} else {
			log.Printf("[Connection Test Faield] %s 连通性测试失败", client)
			allSuccess = false
		}
	}

	return allSuccess
}

// sendTestMessage 发送测试连接消息
func sendTestMessage(clientType, url string, msg map[string]interface{}) bool {
	jsonData, err := json.Marshal(msg)
	if err != nil {
		log.Printf("[%s] JSON 编码失败: %v", clientType, err)
		return false
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		log.Printf("[%s] 创建请求失败: %v", clientType, err)
		return false
	}
	req.Header.Set("Content-Type", "application/json")

	// 修复：将变量名改为 httpClient 避免冲突
	httpClient := &http.Client{Timeout: 5 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		log.Printf("[%s] 发送请求失败: %v", clientType, err)
		return false
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("[%s] 读取响应失败: %v", clientType, err)
		return false
	}

	// 检查响应状态
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("[%s] 返回错误状态码: %d, 响应: %s", clientType, resp.StatusCode, string(body))
		return false
	}

	return true
}

// StartWebhookServer 启动webhook服务
func StartWebhookServer(addr string) {
	router := gin.New()
	router.POST("/webhook-alert", service.GinAlertHandler(Notifiers, EnabledClients))

	srv := &http.Server{
		Addr:    addr,
		Handler: router,
	}

	log.Printf("Webhook Gin 服务启动于 %s\n", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("Webhook Gin 服务启动失败: %v", err)
	}
}
