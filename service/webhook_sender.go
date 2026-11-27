package service

import (
	"alert-webhook/config"
	"alert-webhook/store"
	"alert-webhook/utils"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/alertmanager/template"
)

const (
	defaultRateLimitCooldownWindow = 30 * time.Second
	maxWeChatBatchesPerDelivery    = 5
	maxDingTalkMessageBytes        = 16000
	maxFeishuMessageBytes          = 20000
)

var webhookHTTPTransport = &http.Transport{
	Proxy: http.ProxyFromEnvironment,
	DialContext: (&net.Dialer{
		Timeout:   5 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext,
	ForceAttemptHTTP2:     true,
	MaxIdleConns:          64,
	MaxIdleConnsPerHost:   16,
	MaxConnsPerHost:       32,
	IdleConnTimeout:       60 * time.Second,
	TLSHandshakeTimeout:   5 * time.Second,
	ResponseHeaderTimeout: 8 * time.Second,
	ExpectContinueTimeout: 1 * time.Second,
}

var webhookHTTPClient = &http.Client{
	Timeout:   10 * time.Second,
	Transport: webhookHTTPTransport,
}

type rateLimitCooldown struct {
	mu       sync.Mutex
	duration time.Duration
	until    map[string]time.Time
}

var defaultRateLimitCooldown = &rateLimitCooldown{
	duration: defaultRateLimitCooldownWindow,
	until:    make(map[string]time.Time),
}

func cooldownKey(client, webhookURL string) string {
	return strings.TrimSpace(client) + "|" + strings.TrimSpace(webhookURL)
}

func (c *rateLimitCooldown) Remaining(client, webhookURL string) time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := cooldownKey(client, webhookURL)
	exp, ok := c.until[key]
	if !ok {
		return 0
	}
	rem := time.Until(exp)
	if rem <= 0 {
		delete(c.until, key)
		return 0
	}
	return rem
}

func (c *rateLimitCooldown) Trigger(client, webhookURL string) time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for k, exp := range c.until {
		if !exp.After(now) {
			delete(c.until, k)
		}
	}
	if c.duration <= 0 {
		return 0
	}
	c.until[cooldownKey(client, webhookURL)] = now.Add(c.duration)
	return c.duration
}

func (c *rateLimitCooldown) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	clear(c.until)
}

func (c *rateLimitCooldown) SetDuration(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.duration = d
}

// ResetRateLimitCooldown clears all per-target rate-limit cooldown timers (intended for tests).
func ResetRateLimitCooldown() {
	defaultRateLimitCooldown.Reset()
}

// SetRateLimitCooldownDuration overrides the rate-limit cooldown duration (intended for tests).
func SetRateLimitCooldownDuration(d time.Duration) {
	defaultRateLimitCooldown.SetDuration(d)
}

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
	return sendAlertWithTag(context.Background(), httpClient, client, "", webhookURL, message)
}

func sendAlertWithTag(ctx context.Context, httpClient *http.Client, client, targetLabel, webhookURL string, message interface{}) (int, string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if httpClient == nil {
		httpClient = webhookHTTPClient
	}
	tag := formatClientLogTag(client, targetLabel)

	jsonData, err := json.Marshal(message)
	if err != nil {
		return 0, "", fmt.Errorf("[%s] JSON编码失败: %w", tag, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewBuffer(jsonData))
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
		var apiErr *utils.WebhookAPIError
		if errors.As(err, &apiErr) {
			apiErr.Sanitize(func(s string) string {
				return sanitizeWebhookText(s, webhookURL)
			})
			return resp.StatusCode, respText, apiErr
		}
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
		text, omitted := utils.FormatDingtalkCapped(data, maxDingTalkMessageBytes)
		if omitted > 0 {
			log.Printf("[dingtalk] 告警数量过多 (%d 条)，已折叠其余 %d 条", len(data.Alerts), omitted)
		}
		return DingTalkMessage{
			MsgType: "markdown",
			Markdown: DingTalkMarkdown{
				Title: "Prometheus告警",
				Text:  text,
			},
		}, nil
	case "feishu":
		text, omitted := utils.FormatFeishuCapped(data, maxFeishuMessageBytes)
		if omitted > 0 {
			log.Printf("[feishu] 告警数量过多 (%d 条)，已折叠其余 %d 条", len(data.Alerts), omitted)
		}
		return FeishuMessage{
			MsgType: "text",
			Content: FeishuContent{
				Text: text,
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
	return sendToClientTargetsWithHTTP(context.Background(), webhookHTTPClient, data, clients, targetsMap)
}

// SendToClientTargets sends formatted notifications concurrently across all enabled targets of each client.
func SendToClientTargets(data template.Data, clients []string, notifiers map[string][]config.WebhookTarget) []store.DeliveryResult {
	return SendToClientTargetsContext(context.Background(), data, clients, notifiers)
}

// SendToClientTargetsContext sends formatted notifications concurrently with context cancellation/timeout propagation.
func SendToClientTargetsContext(ctx context.Context, data template.Data, clients []string, notifiers map[string][]config.WebhookTarget) []store.DeliveryResult {
	return sendToClientTargetsWithHTTP(ctx, webhookHTTPClient, data, clients, notifiers)
}

func sendToClientTargetsWithHTTP(ctx context.Context, httpClient *http.Client, data template.Data, clients []string, notifiers map[string][]config.WebhookTarget) []store.DeliveryResult {
	if ctx == nil {
		ctx = context.Background()
	}
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

	var wechatMsgs []string
	if hasWechatTask {
		var omitted int
		wechatMsgs, omitted = utils.RenderWeChatMessages(data, maxWeChatBatchesPerDelivery)
		if omitted > 0 {
			log.Printf("[wechat] 告警数量过多 (%d 条)，已按上限 %d 批折叠其余 %d 条", len(data.Alerts), len(wechatMsgs), omitted)
		}
	}

	var wg sync.WaitGroup
	for _, task := range tasks {
		wg.Add(1)
		go func(t deliveryTask) {
			defer wg.Done()
			results[t.index] = deliverToClient(ctx, httpClient, t.client, t.targetLabel, t.webhookURL, data, wechatMsgs)
		}(task)
	}

	wg.Wait()
	return results
}

func deliverToClient(ctx context.Context, httpClient *http.Client, client, targetLabel, webhookURL string, data template.Data, wechatMsgs []string) store.DeliveryResult {
	start := time.Now()
	tag := formatClientLogTag(client, targetLabel)

	if rem := defaultRateLimitCooldown.Remaining(client, webhookURL); rem > 0 {
		secs := max(1, int(rem.Round(time.Second).Seconds()))
		return store.DeliveryResult{
			Client:      client,
			Target:      targetLabel,
			Success:     false,
			RateLimited: true,
			DurationMs:  time.Since(start).Milliseconds(),
			Error:       fmt.Sprintf("目标处于平台限流冷却中（剩余 %ds），已跳过发送", secs),
		}
	}

	if client == "wechat" {
		msgs := wechatMsgs
		if len(msgs) == 0 {
			msgs, _ = utils.RenderWeChatMessages(data, maxWeChatBatchesPerDelivery)
		}
		log.Printf("[%s] 告警分为 %d 批发送", tag, len(msgs))

		batchSuccess := 0
		rateLimited := false
		var lastStatus int
		var lastResp string
		var errs []string

		for i, content := range msgs {
			if err := ctx.Err(); err != nil {
				errs = append(errs, fmt.Sprintf("第%d批发送取消: %v", i+1, err))
				break
			}

			message := WeChatMessage{
				MsgType: "markdown",
				Markdown: MarkdownMessage{
					Content: content,
				},
			}
			statusCode, respText, err := sendAlertWithTag(ctx, httpClient, client, targetLabel, webhookURL, message)
			if statusCode != 0 {
				lastStatus = statusCode
			}
			if respText != "" {
				lastResp = respText
			}
			if err != nil {
				if utils.IsRateLimitError(err) {
					rateLimited = true
					cooldown := defaultRateLimitCooldown.Trigger(client, webhookURL)
					remaining := len(msgs) - i - 1
					log.Printf("[%s] 第 %d/%d 批触发平台限流，中止剩余 %d 批并进入 %s 冷却: %v", tag, i+1, len(msgs), remaining, cooldown, err)
					errs = append(errs, fmt.Sprintf("第%d批触发限流: %v（已中止后续 %d 批）", i+1, err, remaining))
					break
				}
				log.Printf("[%s] 第 %d/%d 批消息发送失败: %v", tag, i+1, len(msgs), err)
				errs = append(errs, fmt.Sprintf("第%d批: %v", i+1, err))
			} else {
				log.Printf("[%s] 第 %d/%d 批消息发送成功", tag, i+1, len(msgs))
				batchSuccess++
			}

			if i < len(msgs)-1 {
				timer := time.NewTimer(200 * time.Millisecond)
				select {
				case <-ctx.Done():
					timer.Stop()
					errs = append(errs, fmt.Sprintf("批次等待中断: %v", ctx.Err()))
					goto doneWechat
				case <-timer.C:
				}
			}
		}

	doneWechat:
		res := store.DeliveryResult{
			Client:      client,
			Target:      targetLabel,
			Success:     batchSuccess == len(msgs),
			RateLimited: rateLimited,
			StatusCode:  lastStatus,
			DurationMs:  time.Since(start).Milliseconds(),
			Batches:     len(msgs),
			Response:    lastResp,
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

	statusCode, respText, err := sendAlertWithTag(ctx, httpClient, client, targetLabel, webhookURL, message)
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
		if utils.IsRateLimitError(err) {
			res.RateLimited = true
			cooldown := defaultRateLimitCooldown.Trigger(client, webhookURL)
			log.Printf("[%s] 触发平台限流，进入 %s 冷却: %v", tag, cooldown, err)
		} else {
			log.Printf("[%s] 发送告警失败: %v", tag, err)
		}
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

	statusCode, respText, err := sendAlertWithTag(context.Background(), httpClient, client, targetLabel, trimmedURL, msg)
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
