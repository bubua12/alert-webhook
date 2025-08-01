package config

import (
	"alert-webhook/service"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/gin-gonic/gin"
	"io"
	"log"
	"net/http"
	"time"
)

var WebhookUrl string
var WebhookServerPort string

func init() {
	// 加载配置
	configPath := flag.String("config", "./config.yaml", "配置文件路径")
	flag.Parse()

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("配置初始化失败: %v", err)
	}
	fmt.Println("配置初始化成功")

	WebhookUrl = cfg.WeChat.WebhookURL
	WebhookServerPort = cfg.Server.Port
}

// TestWecomConnection 测试企业微信Webhook连通性
func TestWecomConnection() bool {

	msg := map[string]interface{}{
		"msgtype": "markdown",
		"markdown": map[string]string{
			"content": "wecom test message",
		},
	}

	jsonData, err := json.Marshal(msg)
	if err != nil {
		log.Println("JSON 编码失败:", err)
		return false
	}

	req, err := http.NewRequest("POST", WebhookUrl, bytes.NewBuffer(jsonData))
	if err != nil {
		log.Println("创建请求失败:", err)
		return false
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Println("发送请求失败:", err)
		return false
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Println("读取响应失败:", err)
		return false
	}

	// 企业微信响应成功应包含 {"errcode": 0, "errmsg": "ok"}
	var result struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		log.Println("响应解析失败:", err)
		return false
	}

	fmt.Printf("WeCom 响应: %v\n", result)

	return result.ErrCode == 0 && result.ErrMsg == "ok"
}

// StartWebhookServer 使用 Gin 启动 webhook 服务
func StartWebhookServer(addr string) {
	router := gin.New()
	router.POST("/webhook-alert", service.GinAlertHandler(WebhookUrl))

	srv := &http.Server{
		Addr:    addr,
		Handler: router,
	}

	log.Printf("Webhook Gin 服务启动于 %s\n", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("Webhook Gin 服务启动失败: %v", err)
	}
}
