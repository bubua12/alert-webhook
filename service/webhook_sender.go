package service

import (
	"alert-webhook/config"
	"alert-webhook/store"
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

	"github.com/prometheus/alertmanager/template"
)

var webhookHTTPClient = &http.Client{Timeout: 10 * time.Second}

// WeChatMessage 企业微信消息结构
type WeChatMessage struct {
	MsgType  string          `json:"msgtype"`
	Markdown MarkdownMessage `json:"markdown"`
}

type MarkdownMessage struct {
	Content string `json:"content"`
}

// DingTalkMessage 钉钉消息结构
type DingTalkMessage struct {
	MsgType  string           `json:"msgtype"`
	Markdown DingTalkMarkdown `json:"markdown"`
}

type DingTalkMarkdown struct {
	Title string `json:"title"`
	Text  string `json:"text"`
}

// FeishuMessage 飞书消息结构
type FeishuMessage struct {
	MsgType string        `json:"msg_type"`
	Content FeishuContent `json:"content"`
}

type FeishuContent struct {
	Text string `json:"text"`
}

func formatClientLogTag(client, targetLabel string) string {
	c := strings.TrimSpace(client)
	t := strings.TrimSpace(targetLabel)
	if c == "" {
		return t
	}
	if t == "" {
		return c
	}
	return c + "/" + t
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

func deduplicateClients(clients []string) []string {
	if len(clients) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(clients))
	out := make([]string, 0, len(clients))
	for _, c := range clients {
		c = strings.TrimSpace(c)
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	return out
}

// SendAlert 发送告警到指定客户端并校检平台返回码
func SendAlert(client, webhookURL string, message interface{}) error {
	_, _, err := sendAlertWithClient(webhookHTTPClient, client, webhookURL, message)
	return err
}

func sendAlertWithClient(httpClient *http.Client, client, webhookURL string, message interface{}) (int, string, error) {
	return sendAlertWithTag(httpClient, client, "", webhookURL, message)
}

func sendAlertWithTag(httpClient *http.Client, client, targetLabel, webhookURL string, message interface{}) (int, string, error) {
	if httpClient == nil {
		httpClient = webhookHTTPClient
	}
	tag := formatClientLogTag(client, targetLabel)

	jsonData, err := json.Marshal(message)
	if err != nil {
		return 0, "", fmt.Errorf("[%s] JSON编码失败: %w", tag, err)
	}

	req, err := http.NewRequest(http.MethodPost, webhookURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return 0, "", fmt.Errorf("[%s] 创建HTTP请求失败: %s", tag, sanitizeHTTPError(err, webhookURL))
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("[%s] HTTP请求失败: %s", tag, sanitizeHTTPError(err, webhookURL))
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("[%s] 关闭响应体失败: %v", tag, err)
		}
	}()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, "", fmt.Errorf("[%s] 读取响应失败: %s", tag, sanitizeHTTPError(err, webhookURL))
	}

	respText := sanitizeWebhookText(strings.TrimSpace(string(body)), webhookURL)
	if err := utils.ValidateWebhookResponse(client, resp.StatusCode, body); err != nil {
		return resp.StatusCode, respText, errors.New(sanitizeWebhookText(err.Error(), webhookURL))
	}

	return resp.StatusCode, respText, nil
}

// formatMessageForClient formats template.Data into the platform-specific payload.
func formatMessageForClient(client string, data template.Data) (interface{}, error) {
	switch client {
	case "wechat":
		return WeChatMessage{
			MsgType: "markdown",
			Markdown: MarkdownMessage{
				Content: utils.AlertFormatWechat(data),
			},
		}, nil
	case "dingtalk":
		return DingTalkMessage{
			MsgType: "markdown",
			Markdown: DingTalkMarkdown{
				Title: "Prometheus告警",
				Text:  utils.AlertFormatDingtalk(data),
			},
		}, nil
	case "feishu":
		return FeishuMessage{
			MsgType: "text",
			Content: FeishuContent{
				Text: utils.AlertFormatFeishu(data),
			},
		}, nil
	default:
		return nil, fmt.Errorf("未知客户端类型: %s", client)
	}
}

type deliveryTask struct {
	index       int
	client      string
	targetLabel string
	webhookURL  string
}

// SendToClients sends formatted notifications to all target clients concurrently and safely collects results.
func SendToClients(data template.Data, clients []string, notifiers map[string]string) []store.DeliveryResult {
	targetsMap := make(map[string][]config.WebhookTarget, len(notifiers))
	for k, v := range notifiers {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			targetsMap[k] = []config.WebhookTarget{{URL: trimmed}}
		}
	}
	return sendToClientTargetsWithHTTP(webhookHTTPClient, data, clients, targetsMap)
}

// SendToClientTargets sends formatted notifications concurrently across all enabled targets of each client.
func SendToClientTargets(data template.Data, clients []string, notifiers map[string][]config.WebhookTarget) []store.DeliveryResult {
	return sendToClientTargetsWithHTTP(webhookHTTPClient, data, clients, notifiers)
}

func sendToClientTargetsWithHTTP(httpClient *http.Client, data template.Data, clients []string, notifiers map[string][]config.WebhookTarget) []store.DeliveryResult {
	uniqueClients := deduplicateClients(clients)
	if len(uniqueClients) == 0 {
		return []store.DeliveryResult{}
	}

	results := make([]store.DeliveryResult, 0, len(uniqueClients))
	tasks := make([]deliveryTask, 0, len(uniqueClients))
	hasWechatTask := false

	for _, client := range uniqueClients {
		var enabledTargets []config.WebhookTarget
		for _, t := range notifiers[client] {
			if t.IsEnabled() {
				t.Name = strings.TrimSpace(t.Name)
				t.URL = strings.TrimSpace(t.URL)
				enabledTargets = append(enabledTargets, t)
			}
		}

		if len(enabledTargets) == 0 {
			results = append(results, store.DeliveryResult{
				Client:  client,
				Success: false,
				Error:   fmt.Sprintf("客户端 %s 未配置已启用的 Webhook URL", client),
			})
			continue
		}

		for i, t := range enabledTargets {
			targetLabel := config.FormatWebhookTargetLabel(t, i, len(enabledTargets))
			if !config.IsConfiguredWebhookURL(t.URL) {
				results = append(results, store.DeliveryResult{
					Client:  client,
					Target:  targetLabel,
					Success: false,
					Error:   fmt.Sprintf("客户端 %s Webhook URL 未配置或为占位符", client),
				})
				continue
			}

			idx := len(results)
			results = append(results, store.DeliveryResult{})
			tasks = append(tasks, deliveryTask{
				index:       idx,
				client:      client,
				targetLabel: targetLabel,
				webhookURL:  t.URL,
			})
			if client == "wechat" {
				hasWechatTask = true
			}
		}
	}

	if len(tasks) == 0 {
		return results
	}

	var wechatBatches []template.Data
	if hasWechatTask {
		wechatBatches = utils.SplitWeChatAlerts(data)
	}

	var wg sync.WaitGroup
	for _, task := range tasks {
		wg.Add(1)
		go func(t deliveryTask) {
			defer wg.Done()
			results[t.index] = deliverToClient(httpClient, t.client, t.targetLabel, t.webhookURL, data, wechatBatches)
		}(task)
	}

	wg.Wait()
	return results
}

func deliverToClient(httpClient *http.Client, client, targetLabel, webhookURL string, data template.Data, wechatBatches []template.Data) store.DeliveryResult {
	start := time.Now()
	tag := formatClientLogTag(client, targetLabel)

	if client == "wechat" {
		alertBatches := wechatBatches
		if len(alertBatches) == 0 {
			alertBatches = utils.SplitWeChatAlerts(data)
		}
		log.Printf("[%s] 告警分为 %d 批发送", tag, len(alertBatches))

		batchSuccess := 0
		var lastStatus int
		var lastResp string
		var errs []string

		for i, batchData := range alertBatches {
			message := WeChatMessage{
				MsgType: "markdown",
				Markdown: MarkdownMessage{
					Content: utils.AlertFormatWechat(batchData),
				},
			}
			statusCode, respText, err := sendAlertWithTag(httpClient, client, targetLabel, webhookURL, message)
			if statusCode != 0 {
				lastStatus = statusCode
			}
			if respText != "" {
				lastResp = respText
			}
			if err != nil {
				log.Printf("[%s] 第 %d/%d 批消息发送失败: %v", tag, i+1, len(alertBatches), err)
				errs = append(errs, fmt.Sprintf("第%d批: %v", i+1, err))
			} else {
				log.Printf("[%s] 第 %d/%d 批消息发送成功", tag, i+1, len(alertBatches))
				batchSuccess++
			}

			if i < len(alertBatches)-1 {
				time.Sleep(200 * time.Millisecond)
			}
		}

		res := store.DeliveryResult{
			Client:     client,
			Target:     targetLabel,
			Success:    batchSuccess == len(alertBatches),
			StatusCode: lastStatus,
			DurationMs: time.Since(start).Milliseconds(),
			Batches:    len(alertBatches),
			Response:   lastResp,
		}
		if len(errs) > 0 {
			res.Error = strings.Join(errs, "; ")
		}
		return res
	}

	message, err := formatMessageForClient(client, data)
	if err != nil {
		return store.DeliveryResult{
			Client:     client,
			Target:     targetLabel,
			Success:    false,
			DurationMs: time.Since(start).Milliseconds(),
			Error:      err.Error(),
		}
	}

	statusCode, respText, err := sendAlertWithTag(httpClient, client, targetLabel, webhookURL, message)
	res := store.DeliveryResult{
		Client:     client,
		Target:     targetLabel,
		Success:    err == nil,
		StatusCode: statusCode,
		DurationMs: time.Since(start).Milliseconds(),
		Batches:    1,
		Response:   respText,
	}
	if err != nil {
		log.Printf("[%s] 发送告警失败: %v", tag, err)
		res.Error = err.Error()
	} else {
		log.Printf("[%s] 告警发送成功", tag)
	}
	return res
}

// TestNotifierClient sends a lightweight connectivity probe to a single notification channel.
func TestNotifierClient(client, webhookURL string) store.DeliveryResult {
	return TestNotifierClientWithTarget(client, "", webhookURL)
}

// TestNotifierClientWithTarget sends a lightweight connectivity probe to a specific target label and URL.
func TestNotifierClientWithTarget(client, target, webhookURL string) store.DeliveryResult {
	return testNotifierClientWithHTTP(webhookHTTPClient, client, target, webhookURL)
}

// TestNotifierTargets tests all enabled webhook targets for a channel concurrently while preserving deterministic order.
func TestNotifierTargets(client string, targets []config.WebhookTarget) []store.DeliveryResult {
	return testNotifierTargetsWithHTTP(webhookHTTPClient, client, targets)
}

func testNotifierTargetsWithHTTP(httpClient *http.Client, client string, targets []config.WebhookTarget) []store.DeliveryResult {
	enabled := make([]config.WebhookTarget, 0, len(targets))
	for _, t := range targets {
		if t.IsEnabled() {
			t.Name = strings.TrimSpace(t.Name)
			t.URL = strings.TrimSpace(t.URL)
			enabled = append(enabled, t)
		}
	}
	if len(enabled) == 0 {
		return []store.DeliveryResult{{
			Client:  client,
			Success: false,
			Error:   fmt.Sprintf("客户端 %s 未配置已启用的 Webhook URL", client),
		}}
	}

	results := make([]store.DeliveryResult, len(enabled))
	var wg sync.WaitGroup
	for i, t := range enabled {
		targetLabel := config.FormatWebhookTargetLabel(t, i, len(enabled))
		wg.Add(1)
		go func(idx int, label, rawURL string) {
			defer wg.Done()
			results[idx] = testNotifierClientWithHTTP(httpClient, client, label, rawURL)
		}(i, targetLabel, t.URL)
	}
	wg.Wait()
	return results
}

func testNotifierClientWithHTTP(httpClient *http.Client, client, target, webhookURL string) store.DeliveryResult {
	start := time.Now()
	targetLabel := strings.TrimSpace(target)
	trimmedURL := strings.TrimSpace(webhookURL)
	if !config.IsConfiguredWebhookURL(trimmedURL) {
		return store.DeliveryResult{
			Client:  client,
			Target:  targetLabel,
			Success: false,
			Error:   "Webhook URL 未配置或为占位符",
		}
	}

	var msg interface{}
	switch client {
	case "wechat":
		msg = WeChatMessage{
			MsgType: "markdown",
			Markdown: MarkdownMessage{
				Content: "[测试连接] Prometheus Alert Webhook 告警通知渠道连通性测试成功",
			},
		}
	case "dingtalk":
		msg = DingTalkMessage{
			MsgType: "markdown",
			Markdown: DingTalkMarkdown{
				Title: "Prometheus 告警连通性测试",
				Text:  "### 钉钉告警连通性测试\n\n> Prometheus Alert Webhook 告警通知渠道连通性测试成功",
			},
		}
	case "feishu":
		msg = FeishuMessage{
			MsgType: "text",
			Content: FeishuContent{
				Text: "飞书告警连通性测试: Prometheus Alert Webhook 告警通知渠道连通性测试成功",
			},
		}
	default:
		return store.DeliveryResult{
			Client:  client,
			Target:  targetLabel,
			Success: false,
			Error:   fmt.Sprintf("不支持的客户端类型: %s", client),
		}
	}

	statusCode, respText, err := sendAlertWithTag(httpClient, client, targetLabel, trimmedURL, msg)
	res := store.DeliveryResult{
		Client:     client,
		Target:     targetLabel,
		Success:    err == nil,
		StatusCode: statusCode,
		DurationMs: time.Since(start).Milliseconds(),
		Batches:    1,
		Response:   respText,
	}
	if err != nil {
		res.Error = err.Error()
	}
	return res
}

// ComputeDeliveryState derives overall delivery_state from per-channel results.
func ComputeDeliveryState(deliveries []store.DeliveryResult) string {
	if len(deliveries) == 0 {
		return "failed"
	}
	successCount := 0
	for _, d := range deliveries {
		if d.Success {
			successCount++
		}
	}
	switch {
	case successCount == len(deliveries):
		return "sent"
	case successCount == 0:
		return "failed"
	default:
		return "partial"
	}
}

// BuildAlertRecord converts an Alertmanager alert and delivery outcomes into a stored AlertRecord.
func BuildAlertRecord(source string, data template.Data, alert template.Alert, deliveryState, filterReason string, deliveries []store.DeliveryResult) store.AlertRecord {
	loc := utils.GetShanghaiLocation()
	status := alert.Status
	if status == "" {
		status = data.Status
	}
	if status == "" {
		status = "firing"
	}

	labels := make(map[string]string, len(alert.Labels))
	for k, v := range alert.Labels {
		labels[k] = v
	}
	annotations := make(map[string]string, len(alert.Annotations))
	for k, v := range alert.Annotations {
		annotations[k] = v
	}

	severity := labels["severity"]
	rec := store.AlertRecord{
		Timestamp:     time.Now().In(loc).Format(time.RFC3339),
		Source:        source,
		Status:        status,
		DeliveryState: deliveryState,
		AlertName:     labels["alertname"],
		Severity:      severity,
		SeverityCode:  utils.MapSeverity(severity),
		Instance:      labels["instance"],
		Summary:       annotations["summary"],
		Description:   annotations["description"],
		Labels:        labels,
		Annotations:   annotations,
		FilterReason:  filterReason,
		Deliveries:    append([]store.DeliveryResult{}, deliveries...),
	}
	if !alert.StartsAt.IsZero() {
		rec.StartsAt = alert.StartsAt.In(loc).Format(time.RFC3339)
	}
	if !alert.EndsAt.IsZero() {
		rec.EndsAt = alert.EndsAt.In(loc).Format(time.RFC3339)
	}
	return rec
}
