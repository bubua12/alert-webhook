package service

import (
	"alert-webhook/config"
	"alert-webhook/store"
	"alert-webhook/utils"
	"context"
	"encoding/json"
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

// KafkaUIResponse Kafka UI API响应结构
type KafkaUIResponse struct {
	PageCount      int                   `json:"pageCount"`
	ConsumerGroups []ConsumerGroupDetail `json:"consumerGroups"`
}

// ConsumerGroupDetail 消费者组详情
type ConsumerGroupDetail struct {
	GroupID     string      `json:"groupId"`
	Members     int         `json:"members"`
	Topics      int         `json:"topics"`
	Simple      bool        `json:"simple"`
	State       string      `json:"state"`
	ConsumerLag int64       `json:"consumerLag"`
	Coordinator Coordinator `json:"coordinator"`
}

// Coordinator 协调器信息
type Coordinator struct {
	ID   int    `json:"id"`
	Host string `json:"host"`
	Port int    `json:"port"`
}

// KafkaServiceStatus describes the runtime state of KafkaMonitorService.
type KafkaServiceStatus struct {
	Enabled     bool   `json:"enabled"`
	Running     bool   `json:"running"`
	Healthy     bool   `json:"healthy"`
	LastCheck   string `json:"last_check"`
	LastError   string `json:"last_error"`
	LaggedLast  int    `json:"lagged_last"`
	TotalGroups int    `json:"total_groups"`
	TotalAlerts int64  `json:"total_alerts"`
}

var kafkaHTTPClient = &http.Client{Timeout: 10 * time.Second}

// FetchKafkaConsumerGroups queries Kafka UI's consumer-groups endpoint and returns all groups.
func FetchKafkaConsumerGroups(ctx context.Context, httpClient *http.Client, cfg config.KafkaMonitorConfig) ([]ConsumerGroupDetail, error) {
	if httpClient == nil {
		httpClient = kafkaHTTPClient
	}
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.KafkaUIURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("Kafka UI URL 未配置")
	}
	clusterName := strings.TrimSpace(cfg.ClusterName)
	if clusterName == "" {
		return nil, fmt.Errorf("Kafka 集群名称未配置")
	}

	apiURL := fmt.Sprintf("%s/api/clusters/%s/consumer-groups/paged?page=1&perPage=100&search=",
		baseURL,
		url.PathEscape(clusterName),
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("创建 Kafka UI 请求失败: %w", err)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP请求失败: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("[Kafka] 关闭响应体失败: %v", err)
		}
	}()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API返回错误状态码: %d, 响应: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var kafkaResp KafkaUIResponse
	if err := json.Unmarshal(body, &kafkaResp); err != nil {
		return nil, fmt.Errorf("解析JSON失败: %w", err)
	}

	if kafkaResp.ConsumerGroups == nil {
		return []ConsumerGroupDetail{}, nil
	}
	return kafkaResp.ConsumerGroups, nil
}

// KafkaMonitorService Kafka监控服务
type KafkaMonitorService struct {
	cfgMgr     *config.ConfigManager
	config     *config.AppConfig
	alertStore *store.AlertStore
	stopChan   chan struct{}
	stopOnce   sync.Once
	wg         sync.WaitGroup
	httpClient *http.Client

	statusMu sync.RWMutex
	status   KafkaServiceStatus
}

// NewKafkaMonitorService 创建Kafka监控服务实例
func NewKafkaMonitorService(cfgMgr *config.ConfigManager, cfg *config.AppConfig, alertStore *store.AlertStore) *KafkaMonitorService {
	enabled := cfg != nil && cfg.KafkaMonitor.Enabled
	return &KafkaMonitorService{
		cfgMgr:     cfgMgr,
		config:     cfg.Clone(),
		alertStore: alertStore,
		stopChan:   make(chan struct{}),
		httpClient: kafkaHTTPClient,
		status: KafkaServiceStatus{
			Enabled: enabled,
			Healthy: enabled,
		},
	}
}

func (k *KafkaMonitorService) currentConfig() *config.AppConfig {
	if k.cfgMgr != nil {
		return k.cfgMgr.Get()
	}
	return k.config
}

// Start 启动Kafka监控服务
func (k *KafkaMonitorService) Start() {
	cfg := k.currentConfig()
	if cfg == nil || !cfg.KafkaMonitor.Enabled {
		log.Println("Kafka监控功能未启用")
		return
	}

	interval := cfg.KafkaMonitor.CheckInterval
	if interval < 5 {
		log.Printf("[Warn] Kafka监控检查间隔 %d 秒过小，已自动调整为 5 秒", interval)
		interval = 5
	}

	k.statusMu.Lock()
	k.status.Enabled = true
	k.status.Running = true
	k.status.Healthy = true
	k.statusMu.Unlock()

	log.Printf("启动Kafka监控服务，检查间隔: %d秒", interval)

	k.wg.Add(1)
	go k.monitorKafka(interval)
}

// Stop 停止Kafka监控服务（幂等，可多次调用）
func (k *KafkaMonitorService) Stop() {
	k.stopOnce.Do(func() {
		log.Println("正在停止Kafka监控服务...")
		close(k.stopChan)
		k.wg.Wait()
		k.statusMu.Lock()
		k.status.Running = false
		k.statusMu.Unlock()
		log.Println("Kafka监控服务已停止")
	})
}

// Status returns a snapshot of the Kafka monitor service runtime status.
func (k *KafkaMonitorService) Status() KafkaServiceStatus {
	k.statusMu.RLock()
	defer k.statusMu.RUnlock()
	return k.status
}

// CheckNow immediately runs one Kafka consumer-group check and returns updated status.
func (k *KafkaMonitorService) CheckNow() (KafkaServiceStatus, error) {
	err := k.checkAndAlert()
	return k.Status(), err
}

// monitorKafka 监控Kafka的主循环
func (k *KafkaMonitorService) monitorKafka(interval int) {
	defer k.wg.Done()

	ticker := time.NewTicker(time.Duration(interval) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			_ = k.checkAndAlert()
		case <-k.stopChan:
			return
		}
	}
}

// checkAndAlert 检查Kafka消费者组积压并发送告警
func (k *KafkaMonitorService) checkAndAlert() error {
	log.Println("开始检查Kafka消费者组积压...")
	loc := utils.GetShanghaiLocation()
	nowStr := time.Now().In(loc).Format(time.RFC3339)

	cfg := k.currentConfig()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	consumerGroups, err := FetchKafkaConsumerGroups(ctx, k.httpClient, cfg.KafkaMonitor)
	if err != nil {
		log.Printf("获取Kafka消费者组信息失败: %v", err)
		k.statusMu.Lock()
		k.status.LastCheck = nowStr
		k.status.Healthy = false
		k.status.LastError = err.Error()
		k.statusMu.Unlock()
		return err
	}

	var alertGroups []ConsumerGroupDetail
	for _, group := range consumerGroups {
		if shouldIgnoreKafkaGroup(group.GroupID, cfg.KafkaMonitor.IgnoredGroups) {
			continue
		}
		if len(cfg.KafkaMonitor.MonitoredGroups) > 0 && !shouldMonitorKafkaGroup(group.GroupID, cfg.KafkaMonitor.MonitoredGroups) {
			continue
		}
		if group.ConsumerLag > cfg.KafkaMonitor.ConsumerLagThreshold {
			alertGroups = append(alertGroups, group)
			log.Printf("消费者组 %s 积压告警: %d (阈值: %d)",
				group.GroupID, group.ConsumerLag, cfg.KafkaMonitor.ConsumerLagThreshold)
		}
	}

	var sentAlerts int64
	if len(alertGroups) == 0 {
		log.Println("未发现消费者组积压异常")
	} else {
		log.Printf("发现 %d 个消费者组积压异常", len(alertGroups))
		for _, group := range alertGroups {
			if k.generateAndSendAlert(cfg, group) {
				sentAlerts++
			}
		}
	}

	k.statusMu.Lock()
	k.status.LastCheck = nowStr
	k.status.Healthy = true
	k.status.LastError = ""
	k.status.TotalGroups = len(consumerGroups)
	k.status.LaggedLast = len(alertGroups)
	k.status.TotalAlerts += sentAlerts
	k.statusMu.Unlock()

	return nil
}

func shouldIgnoreKafkaGroup(groupID string, ignored []string) bool {
	for _, g := range ignored {
		if groupID == g {
			return true
		}
	}
	return false
}

func shouldMonitorKafkaGroup(groupID string, monitored []string) bool {
	for _, g := range monitored {
		if groupID == g {
			return true
		}
	}
	return false
}

// generateAndSendAlert 生成并发送告警
func (k *KafkaMonitorService) generateAndSendAlert(cfg *config.AppConfig, group ConsumerGroupDetail) bool {
	alert := k.createKafkaAlert(cfg, group)
	data := template.Data{
		Status: "firing",
		Alerts: []template.Alert{alert},
	}

	alertName := alert.Labels["alertname"]
	severity := alert.Labels["severity"]
	decision := cfg.EvaluateAlertFilter(alertName, severity)
	if !decision.Allowed {
		log.Printf("Kafka积压告警 [%s] 被过滤规则拦截: %s", group.GroupID, decision.Reason)
		if k.alertStore != nil {
			k.alertStore.Add(BuildAlertRecord("kafka", data, alert, "filtered", decision.Reason, nil))
		}
		return false
	}

	if len(cfg.Clients) == 0 {
		log.Printf("[Warn] 发现 Kafka 消费者组 %s 积压，但未启用任何通知渠道，仅记录告警", group.GroupID)
		if k.alertStore != nil {
			k.alertStore.Add(BuildAlertRecord("kafka", data, alert, "failed", "", []store.DeliveryResult{{
				Client:  "system",
				Success: false,
				Error:   "未启用任何通知渠道",
			}}))
		}
		return false
	}

	deliveries := SendToClientTargets(data, cfg.Clients, cfg.NotifierTargetsMap())
	deliveryState := ComputeDeliveryState(deliveries)
	if k.alertStore != nil {
		k.alertStore.Add(BuildAlertRecord("kafka", data, alert, deliveryState, "", deliveries))
	}
	return deliveryState == "sent" || deliveryState == "partial"
}

// createKafkaAlert 创建Kafka告警对象
func (k *KafkaMonitorService) createKafkaAlert(cfg *config.AppConfig, group ConsumerGroupDetail) template.Alert {
	now := time.Now().In(utils.GetShanghaiLocation())
	summary := fmt.Sprintf("Kafka消费者组 %s 积压告警", group.GroupID)

	description := fmt.Sprintf(
		"消费者组积压信息:\n"+
			"\t• 消费者组ID: %s\n"+
			"\t• 当前积压量: %d\n"+
			"\t• 告警阈值: %d\n"+
			"\t• 成员数量: %d\n"+
			"\t• 关联主题数: %d\n"+
			"\t• 组状态: %s\n"+
			"\t• 协调器: %s:%d",
		group.GroupID,
		group.ConsumerLag,
		cfg.KafkaMonitor.ConsumerLagThreshold,
		group.Members,
		group.Topics,
		group.State,
		group.Coordinator.Host,
		group.Coordinator.Port,
	)

	return template.Alert{
		Status: "firing",
		Labels: template.KV{
			"alertname":      "Kafka消费者组积压告警",
			"severity":       "warning",
			"consumer_group": group.GroupID,
			"source":         "kafka-ui",
			"alert_type":     "kafka_lag",
			"instance":       group.GroupID,
			"state":          group.State,
		},
		Annotations: template.KV{
			"summary":     summary,
			"description": description,
		},
		StartsAt: now,
		EndsAt:   time.Time{},
	}
}
