package config

import (
	"alert-webhook/utils"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

var (
	globalsMu       sync.RWMutex
	EnabledClients  []string
	Notifiers       map[string]string          // client -> webhook_url
	NotifierTargets map[string][]WebhookTarget // client -> webhook targets
	ServerPort      string
	GlobalConfig    *AppConfig // 全局配置引用
)

// SyncGlobals updates the backward-compatible package globals safely from cfg.
func SyncGlobals(cfg *AppConfig) {
	if cfg == nil {
		return
	}
	snapshot := cfg.Clone()
	notifierMap := snapshot.NotifierURLMap()
	targetMap := snapshot.NotifierTargetsMap()

	globalsMu.Lock()
	GlobalConfig = snapshot
	EnabledClients = append([]string{}, snapshot.Clients...)
	Notifiers = notifierMap
	NotifierTargets = targetMap
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
	notifierTargets := make(map[string][]WebhookTarget, len(NotifierTargets))
	for k, list := range NotifierTargets {
		copied := make([]WebhookTarget, len(list))
		for i, t := range list {
			copied[i] = t.Clone()
		}
		notifierTargets[k] = copied
	}
	globalsMu.RUnlock()

	if len(clients) == 0 {
		return true
	}

	allSuccess := true
	for _, client := range clients {
		targets, hasTargets := notifierTargets[client]
		if !hasTargets {
			if legacyURL, ok := notifiers[client]; ok && strings.TrimSpace(legacyURL) != "" {
				targets = []WebhookTarget{{URL: strings.TrimSpace(legacyURL)}}
			}
		}

		enabledTargets := make([]WebhookTarget, 0, len(targets))
		for _, t := range targets {
			if t.IsEnabled() {
				enabledTargets = append(enabledTargets, t)
			}
		}
		if len(enabledTargets) == 0 {
			log.Printf("客户端 %s 未配置有效 Webhook URL，跳过连通性测试", client)
			continue
		}

		msg, ok := buildTestClientMessage(client)
		if !ok {
			log.Printf("未知客户端类型: %s", client)
			continue
		}

		for i, target := range enabledTargets {
			label := FormatWebhookTargetLabel(target, i, len(enabledTargets))
			displayName := client
			if label != "" {
				displayName = fmt.Sprintf("%s(%s)", client, label)
			}

			if !IsConfiguredWebhookURL(target.URL) {
				log.Printf("客户端 %s 未配置有效 Webhook URL，跳过连通性测试", displayName)
				continue
			}

			if success := sendTestMessage(client, target.URL, msg); success {
				log.Printf("[Connection Test Success] %s 连通性测试成功", displayName)
			} else {
				log.Printf("[Connection Test Failed] %s 连通性测试失败", displayName)
				allSuccess = false
			}
		}
	}

	return allSuccess
}

func buildTestClientMessage(client string) (map[string]interface{}, bool) {
	switch client {
	case "wechat":
		return map[string]interface{}{
			"msgtype": "markdown",
			"markdown": map[string]string{
				"content": "[测试连接] Prometheus Alert Webhook 告警通知渠道",
			},
		}, true
	case "dingtalk":
		return map[string]interface{}{
			"msgtype": "markdown",
			"markdown": map[string]string{
				"title": "Prometheus 告警连通性测试",
				"text":  "钉钉告警连通性测试 (Prometheus Alert Webhook)",
			},
		}, true
	case "feishu":
		return map[string]interface{}{
			"msg_type": "text",
			"content": map[string]string{
				"text": "飞书告警连通性测试 (Prometheus Alert Webhook)",
			},
		}, true
	default:
		return nil, false
	}
}

// sendTestMessage 发送测试连接消息
func sendTestMessage(clientType, rawURL string, msg map[string]interface{}) bool {
	jsonData, err := json.Marshal(msg)
	if err != nil {
		log.Printf("[%s] JSON 编码失败: %v", clientType, err)
		return false
	}

	req, err := http.NewRequest(http.MethodPost, rawURL, bytes.NewBuffer(jsonData))
	if err != nil {
		log.Printf("[%s] 创建请求失败: %s", clientType, sanitizeHTTPError(err, rawURL))
		return false
	}
	req.Header.Set("Content-Type", "application/json")

	httpClient := &http.Client{Timeout: 5 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		log.Printf("[%s] 发送请求失败: %s", clientType, sanitizeHTTPError(err, rawURL))
		return false
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("[%s] 关闭响应体失败: %s", clientType, sanitizeHTTPError(err, rawURL))
		}
	}()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		log.Printf("[%s] 读取响应失败: %s", clientType, sanitizeHTTPError(err, rawURL))
		return false
	}

	if err := utils.ValidateWebhookResponse(clientType, resp.StatusCode, body); err != nil {
		log.Printf("%s", sanitizeWebhookText(err.Error(), rawURL))
		return false
	}

	return true
}

func redactWebhookURL(rawURL string) string {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return ""
	}
	u, err := url.Parse(trimmed)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "[redacted-url]"
	}
	return u.Scheme + "://" + u.Host + "/***"
}

func sanitizeWebhookText(text, webhookURL string) string {
	if text == "" {
		return ""
	}
	redacted := redactWebhookURL(webhookURL)
	if webhookURL != "" {
		text = strings.ReplaceAll(text, webhookURL, redacted)
	}
	if trimmed := strings.TrimSpace(webhookURL); trimmed != "" && trimmed != webhookURL {
		text = strings.ReplaceAll(text, trimmed, redacted)
	}
	return text
}

func sanitizeHTTPError(err error, webhookURL string) string {
	if err == nil {
		return ""
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if urlErr.Err != nil {
			return sanitizeWebhookText(urlErr.Err.Error(), webhookURL)
		}
		return sanitizeWebhookText(urlErr.Op+" "+redactWebhookURL(urlErr.URL), webhookURL)
	}
	return sanitizeWebhookText(err.Error(), webhookURL)
}
