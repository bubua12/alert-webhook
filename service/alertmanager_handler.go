package service

import (
	"alert-webhook/config"
	"alert-webhook/store"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/alertmanager/template"
)

const (
	maxWebhookBodyBytes              int64 = 10 << 20 // 10 MB
	defaultMaxSyncWebhookConcurrency       = 8
	defaultAsyncQueueCapacity              = 64
	defaultMaxAsyncWorkers                 = 4
	defaultWebhookDeliveryTimeout          = 20 * time.Second
	backpressureLogInterval                = 5 * time.Second
)

type webhookDispatchJob struct {
	data   template.Data
	alerts []template.Alert
}

// WebhookDispatcher bounds concurrent webhook deliveries and absorbs alert bursts via an async queue.
type WebhookDispatcher struct {
	cfgMgr          *config.ConfigManager
	alertStore      *store.AlertStore
	syncSem         chan struct{}
	queue           chan webhookDispatchJob
	maxWorkers      int
	deliveryTimeout time.Duration

	baseCtx context.Context
	cancel  context.CancelFunc

	mu            sync.Mutex
	closed        bool
	activeWorkers int
	lastDropLog   time.Time
	wg            sync.WaitGroup
}

// NewWebhookDispatcher creates a WebhookDispatcher with bounded sync concurrency and async burst queue.
func NewWebhookDispatcher(cfgMgr *config.ConfigManager, alertStore *store.AlertStore) *WebhookDispatcher {
	ctx, cancel := context.WithCancel(context.Background())
	return &WebhookDispatcher{
		cfgMgr:          cfgMgr,
		alertStore:      alertStore,
		syncSem:         make(chan struct{}, defaultMaxSyncWebhookConcurrency),
		queue:           make(chan webhookDispatchJob, defaultAsyncQueueCapacity),
		maxWorkers:      defaultMaxAsyncWorkers,
		deliveryTimeout: defaultWebhookDeliveryTimeout,
		baseCtx:         ctx,
		cancel:          cancel,
	}
}

// GinAlertHandler handles incoming Alertmanager webhook payloads using live config and records every alert.
func GinAlertHandler(cfgMgr *config.ConfigManager, alertStore *store.AlertStore) gin.HandlerFunc {
	return NewWebhookDispatcher(cfgMgr, alertStore).Handle
}

// Handle processes incoming Alertmanager webhook payloads with body size limits, batch storage,
// bounded sync delivery, and async burst queuing.
func (d *WebhookDispatcher) Handle(c *gin.Context) {
	if c.Request.Method != http.MethodPost {
		c.String(http.StatusMethodNotAllowed, "仅支持POST请求")
		return
	}

	if c.Request.ContentLength > maxWebhookBodyBytes {
		log.Printf("[Warn] Alertmanager 请求体 Content-Length=%d 超过上限 (%d bytes)，已拒绝", c.Request.ContentLength, maxWebhookBodyBytes)
		c.String(http.StatusRequestEntityTooLarge, "请求体超过大小限制")
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxWebhookBodyBytes)

	var data template.Data
	if err := c.ShouldBindJSON(&data); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			log.Printf("[Warn] Alertmanager 请求体超过上限 (%d bytes)，已拒绝", maxWebhookBodyBytes)
			c.String(http.StatusRequestEntityTooLarge, "请求体超过大小限制")
			return
		}
		log.Printf("解析Alertmanager请求失败: %v", err)
		c.String(http.StatusBadRequest, "无效的请求体")
		return
	}

	if len(data.Alerts) == 0 {
		c.String(http.StatusOK, "无有效告警，无需发送")
		return
	}

	cfg := d.currentConfig()
	allowedAlerts, filteredCount, noneCount := evaluateAndRecordFiltered(cfg, data, d.alertStore)

	log.Printf("收到 Alertmanager 告警 %d 条: 放行 %d 条, 规则拦截 %d 条, 忽略none %d 条",
		len(data.Alerts), len(allowedAlerts), filteredCount, noneCount)

	if len(allowedAlerts) == 0 {
		c.String(http.StatusOK, "所有告警都被过滤，无需发送")
		return
	}

	data.Alerts = allowedAlerts
	if data.Status == "" {
		data.Status = allowedAlerts[0].Status
		if data.Status == "" {
			data.Status = "firing"
		}
	}

	if len(cfg.Clients) == 0 {
		log.Printf("[Warn] 收到 %d 条告警，但当前未启用任何通知渠道，仅记录历史", len(allowedAlerts))
		recordNoClientAlerts(d.alertStore, data, allowedAlerts)
		c.String(http.StatusOK, "未配置通知渠道，告警已记录")
		return
	}

	select {
	case d.syncSem <- struct{}{}:
		defer func() { <-d.syncSem }()

		sendCtx, cancel := context.WithTimeout(c.Request.Context(), d.deliveryTimeout)
		defer cancel()

		deliveries := SendToClientTargetsContext(sendCtx, data, cfg.Clients, cfg.NotifierTargetsMap())
		recordDeliveryResults(d.alertStore, data, allowedAlerts, deliveries)

		failedClients, hasNonRateLimitFailure := summarizeFailedClients(deliveries)
		if len(failedClients) > 0 {
			if !hasNonRateLimitFailure {
				// Return 202 on pure rate-limit/cooldown failures so Alertmanager does not amplify the storm via retries.
				c.String(http.StatusAccepted, fmt.Sprintf("部分渠道触发平台限流已降级: %v", failedClients))
				return
			}
			c.String(http.StatusInternalServerError, fmt.Sprintf("部分客户端发送失败: %v", failedClients))
			return
		}

		c.String(http.StatusOK, "告警已成功发送到所有客户端")

	default:
		if d.enqueue(webhookDispatchJob{data: data, alerts: allowedAlerts}) {
			c.String(http.StatusAccepted, fmt.Sprintf("告警已接收并进入异步发送队列（%d 条）", len(allowedAlerts)))
			return
		}

		d.logBackpressure(len(allowedAlerts))
		backpressureDelivery := []store.DeliveryResult{{
			Client:      "system",
			Success:     false,
			RateLimited: true,
			Error:       "异步发送队列已满，触发背压保护并仅记录历史",
		}}
		recordDeliveryResults(d.alertStore, data, allowedAlerts, backpressureDelivery)
		c.String(http.StatusAccepted, "告警队列已满，已记录历史并跳过外发以防重试风暴")
	}
}

// Shutdown stops accepting new async jobs, cancels in-flight async sends, and waits for active workers.
func (d *WebhookDispatcher) Shutdown(ctx context.Context) {
	if d == nil {
		return
	}
	d.mu.Lock()
	if !d.closed {
		d.closed = true
		d.cancel()
	}
	d.mu.Unlock()

	done := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(done)
	}()

	if ctx == nil {
		<-done
		return
	}
	select {
	case <-done:
	case <-ctx.Done():
	}
}

func (d *WebhookDispatcher) currentConfig() *config.AppConfig {
	if d.cfgMgr != nil {
		return d.cfgMgr.Get()
	}
	return config.DefaultAppConfig()
}

func (d *WebhookDispatcher) enqueue(job webhookDispatchJob) bool {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return false
	}
	select {
	case d.queue <- job:
		if d.activeWorkers < d.maxWorkers {
			d.activeWorkers++
			d.wg.Add(1)
			go d.runWorker()
		}
		d.mu.Unlock()
		return true
	default:
		d.mu.Unlock()
		return false
	}
}

func (d *WebhookDispatcher) runWorker() {
	defer d.wg.Done()
	for {
		d.mu.Lock()
		if d.closed {
			d.activeWorkers--
			d.mu.Unlock()
			return
		}
		select {
		case job := <-d.queue:
			d.mu.Unlock()
			d.processJob(job)
		default:
			d.activeWorkers--
			d.mu.Unlock()
			return
		}
	}
}

func (d *WebhookDispatcher) processJob(job webhookDispatchJob) {
	if d.baseCtx.Err() != nil {
		return
	}
	// Re-read live config and re-evaluate filters so UI config edits apply immediately to queued jobs.
	cfg := d.currentConfig()
	jobData := job.data
	jobData.Alerts = job.alerts

	allowedAlerts, _, _ := evaluateAndRecordFiltered(cfg, jobData, d.alertStore)
	if len(allowedAlerts) == 0 {
		return
	}
	jobData.Alerts = allowedAlerts

	if len(cfg.Clients) == 0 {
		recordNoClientAlerts(d.alertStore, jobData, allowedAlerts)
		return
	}

	sendCtx, cancel := context.WithTimeout(d.baseCtx, d.deliveryTimeout)
	defer cancel()

	deliveries := SendToClientTargetsContext(sendCtx, jobData, cfg.Clients, cfg.NotifierTargetsMap())
	recordDeliveryResults(d.alertStore, jobData, allowedAlerts, deliveries)
}

func (d *WebhookDispatcher) logBackpressure(alertCount int) {
	d.mu.Lock()
	shouldLog := time.Since(d.lastDropLog) >= backpressureLogInterval
	if shouldLog {
		d.lastDropLog = time.Now()
	}
	d.mu.Unlock()

	if shouldLog {
		log.Printf("[Warn] Webhook 异步发送队列已满（容量 %d），触发背压保护：最近批次 %d 条告警仅记录历史",
			cap(d.queue), alertCount)
	}
}

func evaluateAndRecordFiltered(cfg *config.AppConfig, data template.Data, alertStore *store.AlertStore) ([]template.Alert, int, int) {
	allowedAlerts := make([]template.Alert, 0, len(data.Alerts))
	var filteredRecords []store.AlertRecord
	filteredCount := 0
	noneCount := 0

	for _, alert := range data.Alerts {
		alertName := alert.Labels["alertname"]
		severity := alert.Labels["severity"]

		if strings.EqualFold(severity, "none") {
			noneCount++
			if alertStore != nil {
				filteredRecords = append(filteredRecords, BuildAlertRecord("alertmanager", data, alert, "filtered", "告警级别为 none（无效告警）", nil))
			}
			continue
		}

		decision := cfg.EvaluateAlertFilter(alertName, severity)
		if decision.Allowed {
			allowedAlerts = append(allowedAlerts, alert)
		} else {
			filteredCount++
			if alertStore != nil {
				filteredRecords = append(filteredRecords, BuildAlertRecord("alertmanager", data, alert, "filtered", decision.Reason, nil))
			}
		}
	}

	if alertStore != nil && len(filteredRecords) > 0 {
		alertStore.AddBatch(filteredRecords)
	}
	return allowedAlerts, filteredCount, noneCount
}

func recordNoClientAlerts(alertStore *store.AlertStore, data template.Data, alerts []template.Alert) {
	if alertStore == nil || len(alerts) == 0 {
		return
	}
	noClientDelivery := []store.DeliveryResult{{
		Client:  "system",
		Success: false,
		Error:   "未启用任何通知渠道",
	}}
	recs := make([]store.AlertRecord, 0, len(alerts))
	for _, alert := range alerts {
		recs = append(recs, BuildAlertRecord("alertmanager", data, alert, "failed", "", noClientDelivery))
	}
	alertStore.AddBatch(recs)
}

func recordDeliveryResults(alertStore *store.AlertStore, data template.Data, alerts []template.Alert, deliveries []store.DeliveryResult) {
	if alertStore == nil || len(alerts) == 0 {
		return
	}
	deliveryState := ComputeDeliveryState(deliveries)
	recs := make([]store.AlertRecord, 0, len(alerts))
	for _, alert := range alerts {
		recs = append(recs, BuildAlertRecord("alertmanager", data, alert, deliveryState, "", deliveries))
	}
	alertStore.AddBatch(recs)
}

func summarizeFailedClients(deliveries []store.DeliveryResult) ([]string, bool) {
	var failedClients []string
	hasNonRateLimitFailure := false
	for _, d := range deliveries {
		if !d.Success {
			if !d.RateLimited {
				hasNonRateLimitFailure = true
			}
			switch {
			case d.Target != "" && d.Client == "wechat" && d.Batches > 1:
				failedClients = append(failedClients, fmt.Sprintf("%s(%s: %s)", d.Client, d.Target, d.Error))
			case d.Target != "":
				failedClients = append(failedClients, fmt.Sprintf("%s(%s)", d.Client, d.Target))
			case d.Client == "wechat" && d.Batches > 1:
				failedClients = append(failedClients, fmt.Sprintf("%s(%s)", d.Client, d.Error))
			default:
				failedClients = append(failedClients, d.Client)
			}
		}
	}
	return failedClients, hasNonRateLimitFailure
}
