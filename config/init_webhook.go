package config

import (
	"alert-webhook/utils"
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

var (
	globalsMu      sync.RWMutex
	EnabledClients []string
	Notifiers      map[string]string // client -> webhook_url
	ServerPort     string
	GlobalConfig   *AppConfig // 全局配置引用
)

// SyncGlobals updates the backward-compatible package globals safely from cfg.
func SyncGlobals(cfg *AppConfig) {
	if cfg == nil {
		return
	}
	snapshot := cfg.Clone()
	notifierMap := snapshot.NotifierURLMap()

	globalsMu.Lock()
	GlobalConfig = snapshot
	EnabledClients = append([]string{}, snapshot.Clients...)
	Notifiers = notifierMap
	ServerPort = snapshot.Server.Port
	globalsMu.Unlock()
}

// TestClientsConnection 测试所有启用的客户端连通性
func TestClientsConnection() bool {
	globalsMu.RLock()
	clients := append([]string{}, EnabledClients...)
	notifiers := make(map[string]string, len(Notifiers))
	for k, v := range Notifiers {
		notifiers[k] = v
	}
	globalsMu.RUnlock()

	if len(clients) == 0 {
		return true
	}

	allSuccess := true
	for _, client := range clients {
		url, ok := notifiers[client]
		if !ok || !IsConfiguredWebhookURL(url) {
			log.Printf("客户端 %s 未配置有效 Webhook URL，跳过连通性测试", client)
			continue
		}

		var msg map[string]interface{}
		switch client {
		case "wechat":
			msg = map[string]interface{}{
				"msgtype": "markdown",
				"markdown": map[string]string{
					"content": "[测试连接] Prometheus Alert Webhook 告警通知渠道",
				},
			}
		case "dingtalk":
			msg = map[string]interface{}{
				"msgtype": "markdown",
				"markdown": map[string]string{
					"title": "Prometheus 告警连通性测试",
					"text":  "钉钉告警连通性测试 (Prometheus Alert Webhook)",
				},
			}
		case "feishu":
			msg = map[string]interface{}{
				"msg_type": "text",
				"content": map[string]string{
					"text": "飞书告警连通性测试 (Prometheus Alert Webhook)",
				},
			}
		default:
			log.Printf("未知客户端类型: %s", client)
			continue
		}

		if success := sendTestMessage(client, url, msg); success {
			log.Printf("[Connection Test Success] %s 连通性测试成功", client)
		} else {
			log.Printf("[Connection Test Failed] %s 连通性测试失败", client)
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

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewBuffer(jsonData))
	if err != nil {
		log.Printf("[%s] 创建请求失败: %v", clientType, err)
		return false
	}
	req.Header.Set("Content-Type", "application/json")

	httpClient := &http.Client{Timeout: 5 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		log.Printf("[%s] 发送请求失败: %v", clientType, err)
		return false
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("[%s] 关闭响应体失败: %v", clientType, err)
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("[%s] 读取响应失败: %v", clientType, err)
		return false
	}

	if err := utils.ValidateWebhookResponse(clientType, resp.StatusCode, body); err != nil {
		log.Printf("%v", err)
		return false
	}

	return true
}
