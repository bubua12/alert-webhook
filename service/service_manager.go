package service

import (
	"alert-webhook/config"
	"alert-webhook/console"
	"alert-webhook/store"
	"context"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
	"time"
)

// ServicesStatus aggregates runtime statuses of background monitoring services.
type ServicesStatus struct {
	TrafficAlert TrafficServiceStatus `json:"traffic_alert"`
	KafkaMonitor KafkaServiceStatus   `json:"kafka_monitor"`
}

// ClickHouseTestResult represents the outcome of POST /api/test/clickhouse.
type ClickHouseTestResult struct {
	OK         bool   `json:"ok"`
	LatencyMs  int64  `json:"latency_ms"`
	RecentRows uint64 `json:"recent_rows"`
	Message    string `json:"message,omitempty"`
	Error      string `json:"error,omitempty"`
}

// KafkaTestResult represents the outcome of POST /api/test/kafka.
type KafkaTestResult struct {
	OK         bool                  `json:"ok"`
	LatencyMs  int64                 `json:"latency_ms"`
	GroupCount int                   `json:"group_count"`
	Groups     []ConsumerGroupDetail `json:"groups"`
	Error      string                `json:"error,omitempty"`
}

// ServiceManager manages the lifecycle and hot-reload of background monitoring services.
type ServiceManager struct {
	mu                  sync.Mutex
	cfgMgr              *config.ConfigManager
	alertStore          *store.AlertStore
	clickhouseService   *ClickHouseService
	trafficAlertService *TrafficAlertService
	kafkaMonitorService *KafkaMonitorService
	trafficInitErr      string
	kafkaInitErr        string

	hasLastConfig  bool
	lastClickHouse config.ClickHouseConfig
	lastTraffic    config.TrafficAlertConfig
	lastKafka      config.KafkaMonitorConfig

	reloadMu      sync.Mutex
	pendingReload *config.AppConfig
	hasPending    bool
	reloadRunning bool
	reloadClosed  bool
	reloadWg      sync.WaitGroup
}

// NewServiceManager creates a new ServiceManager.
func NewServiceManager(cfgMgr *config.ConfigManager, alertStore *store.AlertStore) *ServiceManager {
	return &ServiceManager{
		cfgMgr:     cfgMgr,
		alertStore: alertStore,
	}
}

// InitializeAll starts enabled background services based on cfg.
func (sm *ServiceManager) InitializeAll(cfg *config.AppConfig) {
	sm.Reload(cfg)
}

func kafkaMonitorConfigEqual(a, b config.KafkaMonitorConfig) bool {
	return a.Enabled == b.Enabled &&
		a.KafkaUIURL == b.KafkaUIURL &&
		a.ClusterName == b.ClusterName &&
		a.CheckInterval == b.CheckInterval &&
		a.ConsumerLagThreshold == b.ConsumerLagThreshold &&
		slices.Equal(a.MonitoredGroups, b.MonitoredGroups) &&
		slices.Equal(a.IgnoredGroups, b.IgnoredGroups)
}

func cloneKafkaMonitorConfig(src config.KafkaMonitorConfig) config.KafkaMonitorConfig {
	cloned := src
	cloned.MonitoredGroups = append([]string{}, src.MonitoredGroups...)
	cloned.IgnoredGroups = append([]string{}, src.IgnoredGroups...)
	return cloned
}

// Reload selectively reconciles running background services against newCfg.
// Unchanged ClickHouse/Traffic or Kafka configurations keep their existing services running untouched.
func (sm *ServiceManager) Reload(newCfg *config.AppConfig) {
	if newCfg == nil && sm.cfgMgr != nil {
		newCfg = sm.cfgMgr.Get()
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	if newCfg == nil {
		sm.stopLocked()
		sm.trafficInitErr = ""
		sm.kafkaInitErr = ""
		sm.hasLastConfig = false
		return
	}

	trafficChanged := !sm.hasLastConfig ||
		sm.lastClickHouse != newCfg.ClickHouse ||
		sm.lastTraffic != newCfg.TrafficAlert

	if !newCfg.TrafficAlert.Enabled {
		if sm.trafficAlertService != nil || sm.clickhouseService != nil {
			sm.stopTrafficLocked()
			sm.trafficInitErr = ""
		} else if !sm.hasLastConfig {
			sm.trafficInitErr = ""
			sm.initTrafficLocked(newCfg)
		} else {
			sm.trafficInitErr = ""
		}
	} else if trafficChanged || sm.trafficAlertService == nil || sm.clickhouseService == nil {
		sm.stopTrafficLocked()
		sm.trafficInitErr = ""
		sm.initTrafficLocked(newCfg)
	}

	kafkaChanged := !sm.hasLastConfig || !kafkaMonitorConfigEqual(sm.lastKafka, newCfg.KafkaMonitor)
	if !newCfg.KafkaMonitor.Enabled {
		if sm.kafkaMonitorService != nil {
			sm.stopKafkaLocked()
			sm.kafkaInitErr = ""
		} else if !sm.hasLastConfig {
			sm.kafkaInitErr = ""
			sm.initKafkaLocked(newCfg)
		} else {
			sm.kafkaInitErr = ""
		}
	} else if kafkaChanged || sm.kafkaMonitorService == nil {
		sm.stopKafkaLocked()
		sm.kafkaInitErr = ""
		sm.initKafkaLocked(newCfg)
	}

	sm.hasLastConfig = true
	sm.lastClickHouse = newCfg.ClickHouse
	sm.lastTraffic = newCfg.TrafficAlert
	sm.lastKafka = cloneKafkaMonitorConfig(newCfg.KafkaMonitor)
}

// ReloadAsync schedules a non-blocking background reload, coalescing rapid config updates.
func (sm *ServiceManager) ReloadAsync(newCfg *config.AppConfig) {
	var snapshot *config.AppConfig
	if newCfg != nil {
		snapshot = newCfg.Clone()
	}

	sm.reloadMu.Lock()
	if sm.reloadClosed {
		sm.reloadMu.Unlock()
		return
	}
	sm.pendingReload = snapshot
	sm.hasPending = true
	if !sm.reloadRunning {
		sm.reloadRunning = true
		sm.reloadWg.Add(1)
		go sm.runReloadLoop()
	}
	sm.reloadMu.Unlock()
}

func (sm *ServiceManager) runReloadLoop() {
	defer sm.reloadWg.Done()
	for {
		sm.reloadMu.Lock()
		if sm.reloadClosed || !sm.hasPending {
			sm.reloadRunning = false
			sm.reloadMu.Unlock()
			return
		}
		cfg := sm.pendingReload
		sm.pendingReload = nil
		sm.hasPending = false
		sm.reloadMu.Unlock()

		sm.Reload(cfg)
	}
}

// InitializeTrafficAlert initializes only the traffic alert service using current config.
func (sm *ServiceManager) InitializeTrafficAlert() {
	var cfg *config.AppConfig
	if sm.cfgMgr != nil {
		cfg = sm.cfgMgr.Get()
	} else {
		cfg = config.GlobalConfig
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.initTrafficLocked(cfg)
}

// InitializeKafkaMonitor initializes only the Kafka monitor service using current config.
func (sm *ServiceManager) InitializeKafkaMonitor() {
	var cfg *config.AppConfig
	if sm.cfgMgr != nil {
		cfg = sm.cfgMgr.Get()
	} else {
		cfg = config.GlobalConfig
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.initKafkaLocked(cfg)
}

func (sm *ServiceManager) initTrafficLocked(cfg *config.AppConfig) {
	if cfg == nil || !cfg.TrafficAlert.Enabled {
		log.Println("大流量告警功能未配置启用，将不开启流量监控功能")
		console.Warning("[Warning]", "大流量告警功能未启用，将不开启流量监控功能")
		return
	}

	chSvc, err := NewClickHouseService(cfg)
	if err != nil {
		log.Printf("ClickHouse服务初始化失败: %v", err)
		console.Error("[Error]", "ClickHouse连接失败，大流量告警功能将被禁用")
		sm.trafficInitErr = err.Error()
		return
	}

	if err := chSvc.TestConnection(); err != nil {
		log.Printf("ClickHouse连接测试失败: %v", err)
		console.Error("[Error]", "ClickHouse连接测试失败，大流量告警功能将被禁用")
		_ = chSvc.Close()
		sm.trafficInitErr = err.Error()
		return
	}

	sm.clickhouseService = chSvc
	sm.trafficAlertService = NewTrafficAlertService(chSvc, sm.cfgMgr, cfg, sm.alertStore)
	sm.trafficAlertService.Start()
	console.Success("[Success]", "大流量告警服务启动成功")
}

func (sm *ServiceManager) initKafkaLocked(cfg *config.AppConfig) {
	if cfg == nil || !cfg.KafkaMonitor.Enabled {
		log.Println("Kafka监控功能未配置启用，将不开启Kafka监控功能")
		console.Warning("[Warning]", "Kafka监控功能未启用，将不开启Kafka监控功能")
		return
	}

	if strings.TrimSpace(cfg.KafkaMonitor.KafkaUIURL) == "" {
		msg := "Kafka UI URL未配置，Kafka监控功能将被禁用"
		log.Println(msg)
		console.Error("[Error]", "Kafka UI URL未配置")
		sm.kafkaInitErr = msg
		return
	}

	if strings.TrimSpace(cfg.KafkaMonitor.ClusterName) == "" {
		msg := "Kafka集群名称未配置，Kafka监控功能将被禁用"
		log.Println(msg)
		console.Error("[Error]", "Kafka集群名称未配置")
		sm.kafkaInitErr = msg
		return
	}

	sm.kafkaMonitorService = NewKafkaMonitorService(sm.cfgMgr, cfg, sm.alertStore)
	sm.kafkaMonitorService.Start()
	console.Success("[Success]", "Kafka监控服务启动成功")
}

func (sm *ServiceManager) stopTrafficLocked() {
	if sm.trafficAlertService != nil {
		sm.trafficAlertService.Stop()
		sm.trafficAlertService = nil
	}
	if sm.clickhouseService != nil {
		if err := sm.clickhouseService.Close(); err != nil {
			log.Printf("关闭ClickHouse连接失败: %v", err)
		}
		sm.clickhouseService = nil
	}
}

func (sm *ServiceManager) stopKafkaLocked() {
	if sm.kafkaMonitorService != nil {
		sm.kafkaMonitorService.Stop()
		sm.kafkaMonitorService = nil
	}
}

func (sm *ServiceManager) stopLocked() {
	sm.stopTrafficLocked()
	sm.stopKafkaLocked()
}

// Shutdown 优雅关闭所有服务
func (sm *ServiceManager) Shutdown() {
	log.Println("正在关闭服务管理器...")
	sm.reloadMu.Lock()
	sm.reloadClosed = true
	sm.pendingReload = nil
	sm.hasPending = false
	sm.reloadMu.Unlock()
	sm.reloadWg.Wait()

	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.stopLocked()
}

// IsTrafficAlertEnabled 检查大流量告警是否已启用并运行
func (sm *ServiceManager) IsTrafficAlertEnabled() bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.trafficAlertService != nil && sm.clickhouseService != nil
}

// Status returns combined runtime status of TrafficAlertService and KafkaMonitorService.
func (sm *ServiceManager) Status() ServicesStatus {
	var cfg *config.AppConfig
	if sm.cfgMgr != nil {
		cfg = sm.cfgMgr.Get()
	}

	sm.mu.Lock()
	trafficSvc := sm.trafficAlertService
	kafkaSvc := sm.kafkaMonitorService
	trafficErr := sm.trafficInitErr
	kafkaErr := sm.kafkaInitErr
	sm.mu.Unlock()

	var trafficStatus TrafficServiceStatus
	if trafficSvc != nil {
		trafficStatus = trafficSvc.Status()
	} else if cfg != nil {
		trafficStatus = TrafficServiceStatus{
			Enabled:   cfg.TrafficAlert.Enabled,
			Running:   false,
			Healthy:   !cfg.TrafficAlert.Enabled && trafficErr == "",
			LastError: trafficErr,
		}
		if cfg.TrafficAlert.Enabled && trafficErr == "" {
			trafficStatus.Healthy = false
		}
	}

	var kafkaStatus KafkaServiceStatus
	if kafkaSvc != nil {
		kafkaStatus = kafkaSvc.Status()
	} else if cfg != nil {
		kafkaStatus = KafkaServiceStatus{
			Enabled:   cfg.KafkaMonitor.Enabled,
			Running:   false,
			Healthy:   !cfg.KafkaMonitor.Enabled && kafkaErr == "",
			LastError: kafkaErr,
		}
		if cfg.KafkaMonitor.Enabled && kafkaErr == "" {
			kafkaStatus.Healthy = false
		}
	}

	return ServicesStatus{
		TrafficAlert: trafficStatus,
		KafkaMonitor: kafkaStatus,
	}
}

// TriggerTrafficCheck triggers an immediate traffic anomaly check if the service is running.
func (sm *ServiceManager) TriggerTrafficCheck() (TrafficServiceStatus, error) {
	sm.mu.Lock()
	trafficSvc := sm.trafficAlertService
	sm.mu.Unlock()

	if trafficSvc == nil {
		return sm.Status().TrafficAlert, fmt.Errorf("大流量监控服务未运行")
	}
	return trafficSvc.CheckNow()
}

// TriggerKafkaCheck triggers an immediate Kafka consumer lag check if the service is running.
func (sm *ServiceManager) TriggerKafkaCheck() (KafkaServiceStatus, error) {
	sm.mu.Lock()
	kafkaSvc := sm.kafkaMonitorService
	sm.mu.Unlock()

	if kafkaSvc == nil {
		return sm.Status().KafkaMonitor, fmt.Errorf("Kafka 监控服务未运行")
	}
	return kafkaSvc.CheckNow()
}

// TestClickHouse validates ClickHouse connectivity and queries recent row count.
func (sm *ServiceManager) TestClickHouse(chCfg config.ClickHouseConfig) ClickHouseTestResult {
	start := time.Now()
	if strings.TrimSpace(chCfg.Host) == "" {
		return ClickHouseTestResult{
			OK:        false,
			LatencyMs: time.Since(start).Milliseconds(),
			Error:     "ClickHouse host 不能为空",
		}
	}
	if chCfg.Port <= 0 || chCfg.Port > 65535 {
		return ClickHouseTestResult{
			OK:        false,
			LatencyMs: time.Since(start).Milliseconds(),
			Error:     "ClickHouse 端口无效",
		}
	}

	tmpCfg := &config.AppConfig{ClickHouse: chCfg}
	svc, err := NewClickHouseService(tmpCfg)
	if err != nil {
		return ClickHouseTestResult{
			OK:        false,
			LatencyMs: time.Since(start).Milliseconds(),
			Error:     err.Error(),
		}
	}
	defer func() {
		_ = svc.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rows, err := svc.TestConnectionWithCount(ctx)
	if err != nil {
		return ClickHouseTestResult{
			OK:        false,
			LatencyMs: time.Since(start).Milliseconds(),
			Error:     err.Error(),
		}
	}

	return ClickHouseTestResult{
		OK:         true,
		LatencyMs:  time.Since(start).Milliseconds(),
		RecentRows: rows,
		Message:    fmt.Sprintf("连接成功，最近 1 分钟有 %d 条访问日志", rows),
	}
}

// TestKafkaUI tests connectivity to Kafka UI and returns discovered consumer groups.
func (sm *ServiceManager) TestKafkaUI(kCfg config.KafkaMonitorConfig) KafkaTestResult {
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	groups, err := FetchKafkaConsumerGroups(ctx, kafkaHTTPClient, kCfg)
	if err != nil {
		return KafkaTestResult{
			OK:         false,
			LatencyMs:  time.Since(start).Milliseconds(),
			GroupCount: 0,
			Groups:     []ConsumerGroupDetail{},
			Error:      err.Error(),
		}
	}

	return KafkaTestResult{
		OK:         true,
		LatencyMs:  time.Since(start).Milliseconds(),
		GroupCount: len(groups),
		Groups:     groups,
	}
}
