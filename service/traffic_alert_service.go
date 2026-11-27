package service

import (
	"alert-webhook/config"
	"alert-webhook/store"
	"alert-webhook/utils"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/prometheus/alertmanager/template"
)

// TrafficServiceStatus describes the runtime state of TrafficAlertService.
type TrafficServiceStatus struct {
	Enabled       bool   `json:"enabled"`
	Running       bool   `json:"running"`
	Healthy       bool   `json:"healthy"`
	LastCheck     string `json:"last_check"`
	LastError     string `json:"last_error"`
	AnomaliesLast int    `json:"anomalies_last"`
	TotalAlerts   int64  `json:"total_alerts"`
}

// TrafficAlertService 流量告警服务
type TrafficAlertService struct {
	clickhouseService *ClickHouseService
	cfgMgr            *config.ConfigManager
	config            *config.AppConfig
	alertStore        *store.AlertStore
	stopChan          chan struct{}
	stopOnce          sync.Once
	wg                sync.WaitGroup

	statusMu sync.RWMutex
	status   TrafficServiceStatus
}

// NewTrafficAlertService 创建流量告警服务实例
func NewTrafficAlertService(clickhouseService *ClickHouseService, cfgMgr *config.ConfigManager, cfg *config.AppConfig, alertStore *store.AlertStore) *TrafficAlertService {
	enabled := cfg != nil && cfg.TrafficAlert.Enabled
	return &TrafficAlertService{
		clickhouseService: clickhouseService,
		cfgMgr:            cfgMgr,
		config:            cfg.Clone(),
		alertStore:        alertStore,
		stopChan:          make(chan struct{}),
		status: TrafficServiceStatus{
			Enabled: enabled,
			Healthy: enabled,
		},
	}
}

func (t *TrafficAlertService) currentConfig() *config.AppConfig {
	if t.cfgMgr != nil {
		return t.cfgMgr.Get()
	}
	return t.config
}

// Start 启动流量告警服务
func (t *TrafficAlertService) Start() {
	cfg := t.currentConfig()
	if cfg == nil || !cfg.TrafficAlert.Enabled {
		log.Println("大流量告警功能未启用")
		return
	}

	interval := cfg.TrafficAlert.CheckInterval
	if interval < 5 {
		log.Printf("[Warn] 大流量告警检查间隔 %d 秒过小，已自动调整为 5 秒", interval)
		interval = 5
	}

	t.statusMu.Lock()
	t.status.Enabled = true
	t.status.Running = true
	t.status.Healthy = true
	t.statusMu.Unlock()

	log.Printf("启动大流量告警服务，检查间隔: %d秒", interval)

	t.wg.Add(1)
	go t.monitorTraffic(interval)
}

// Stop 停止流量告警服务（幂等，可多次调用）
func (t *TrafficAlertService) Stop() {
	t.stopOnce.Do(func() {
		log.Println("正在停止大流量告警服务...")
		close(t.stopChan)
		t.wg.Wait()
		t.statusMu.Lock()
		t.status.Running = false
		t.statusMu.Unlock()
		log.Println("大流量告警服务已停止")
	})
}

// Status returns a snapshot of the traffic alert service runtime status.
func (t *TrafficAlertService) Status() TrafficServiceStatus {
	t.statusMu.RLock()
	defer t.statusMu.RUnlock()
	return t.status
}

// CheckNow immediately triggers a traffic anomaly check and returns updated status.
func (t *TrafficAlertService) CheckNow() (TrafficServiceStatus, error) {
	err := t.checkAndAlert()
	return t.Status(), err
}

// monitorTraffic 监控流量的主循环
func (t *TrafficAlertService) monitorTraffic(interval int) {
	defer t.wg.Done()

	ticker := time.NewTicker(time.Duration(interval) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			_ = t.checkAndAlert()
		case <-t.stopChan:
			return
		}
	}
}

// checkAndAlert 检查流量异常并发送告警
func (t *TrafficAlertService) checkAndAlert() error {
	log.Println("开始检查大流量异常...")
	loc := utils.GetShanghaiLocation()
	nowStr := time.Now().In(loc).Format(time.RFC3339)

	trafficStats, err := t.clickhouseService.CheckTrafficAnomalies()
	if err != nil {
		log.Printf("查询流量异常失败: %v", err)
		t.statusMu.Lock()
		t.status.LastCheck = nowStr
		t.status.Healthy = false
		t.status.LastError = err.Error()
		t.statusMu.Unlock()
		return err
	}

	var sentAlerts int64
	if len(trafficStats) == 0 {
		log.Println("未发现流量异常")
	} else {
		log.Printf("发现 %d 个流量异常", len(trafficStats))
		for _, stat := range trafficStats {
			if t.generateAndSendAlert(stat) {
				sentAlerts++
			}
		}
	}

	t.statusMu.Lock()
	t.status.LastCheck = nowStr
	t.status.Healthy = true
	t.status.LastError = ""
	t.status.AnomaliesLast = len(trafficStats)
	t.status.TotalAlerts += sentAlerts
	t.statusMu.Unlock()

	return nil
}

// generateAndSendAlert 为流量异常生成并发送告警
func (t *TrafficAlertService) generateAndSendAlert(stat TrafficStats) bool {
	cfg := t.currentConfig()
	largeRequests, err := t.clickhouseService.GetRecentLargeRequests(stat.Domain, stat.TopPath, 5)
	if err != nil {
		log.Printf("获取大请求详情失败: %v", err)
	}

	alert := t.createTrafficAlert(cfg, stat, largeRequests)
	data := template.Data{
		Status: "firing",
		Alerts: []template.Alert{alert},
	}

	alertName := alert.Labels["alertname"]
	severity := alert.Labels["severity"]
	decision := cfg.EvaluateAlertFilter(alertName, severity)
	if !decision.Allowed {
		log.Printf("大流量告警 [%s] 被过滤规则拦截: %s", stat.Domain, decision.Reason)
		if t.alertStore != nil {
			t.alertStore.Add(BuildAlertRecord("traffic", data, alert, "filtered", decision.Reason, nil))
		}
		return false
	}

	if len(cfg.Clients) == 0 {
		log.Printf("[Warn] 发现大流量异常 (%s)，但未启用任何通知渠道，仅记录告警", stat.Domain)
		if t.alertStore != nil {
			t.alertStore.Add(BuildAlertRecord("traffic", data, alert, "failed", "", []store.DeliveryResult{{
				Client:  "system",
				Success: false,
				Error:   "未启用任何通知渠道",
			}}))
		}
		return false
	}

	deliveries := SendToClientTargets(data, cfg.Clients, cfg.NotifierTargetsMap())
	deliveryState := ComputeDeliveryState(deliveries)
	if t.alertStore != nil {
		t.alertStore.Add(BuildAlertRecord("traffic", data, alert, deliveryState, "", deliveries))
	}
	return deliveryState == "sent" || deliveryState == "partial"
}

// createTrafficAlert 创建流量告警对象
func (t *TrafficAlertService) createTrafficAlert(cfg *config.AppConfig, stat TrafficStats, largeRequests []NginxAccessLog) template.Alert {
	now := time.Now().In(utils.GetShanghaiLocation())
	summary := fmt.Sprintf("域名 %s 路径 %s 发现大流量异常", stat.Domain, stat.TopPath)

	description := fmt.Sprintf(
		"在过去 %d 分钟内，流量统计信息如下\n"+
			"\t• 总请求数: %d\n"+
			"\t• 大请求数量: %d (阈值: %s)\n"+
			"\t• 大响应数量: %d (阈值: %s)\n"+
			"\t• 平均请求大小: %.2f 字节\n"+
			"\t• 平均响应大小: %.2f 字节\n"+
			"\t• 最大请求大小: %d 字节\n"+
			"\t• 最大响应大小: %d 字节",
		cfg.TrafficAlert.TimeWindow,
		stat.TotalCount,
		stat.LargeRequestCount,
		formatBytes(cfg.TrafficAlert.RequestSizeThreshold),
		stat.LargeResponseCount,
		formatBytes(cfg.TrafficAlert.ResponseSizeThreshold),
		stat.AvgRequestSize,
		stat.AvgResponseSize,
		stat.MaxRequestSize,
		stat.MaxResponseSize,
	)

	if len(largeRequests) > 0 {
		description += "\n最近的大请求示例:"
		for i, req := range largeRequests {
			if i >= 3 {
				break
			}
			description += fmt.Sprintf(
				"\n%d. %s %s %s (请求:%d字节, 响应:%d字节, 耗时:%.3fs)",
				i+1,
				req.Timestamp.In(utils.GetShanghaiLocation()).Format("15:04:05"),
				req.RequestMethod,
				req.Path,
				req.RequestLength,
				req.ResponseLength,
				req.ResponseTime,
			)
		}
	}

	return template.Alert{
		Status: "firing",
		Labels: template.KV{
			"alertname":  "异常流量告警",
			"severity":   "warning",
			"domain":     stat.Domain,
			"top_path":   stat.TopPath,
			"source":     "clickhouse",
			"alert_type": "traffic",
			"instance":   stat.Domain,
		},
		Annotations: template.KV{
			"summary":     summary,
			"description": description,
		},
		StartsAt: now,
		EndsAt:   time.Time{},
	}
}

// formatBytes 格式化字节数为可读格式
func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}
