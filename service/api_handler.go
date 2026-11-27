package service

import (
	"alert-webhook/config"
	"alert-webhook/store"
	"alert-webhook/utils"
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/alertmanager/template"
)

const appVersion = "1.2.0"

// APIHandler implements the REST control-plane and health endpoints.
type APIHandler struct {
	cfgMgr     *config.ConfigManager
	alertStore *store.AlertStore
	logBuffer  *store.LogBuffer
	svcMgr     *ServiceManager
	startedAt  time.Time
}

// NewAPIHandler creates an APIHandler bound to runtime dependencies.
func NewAPIHandler(cfgMgr *config.ConfigManager, alertStore *store.AlertStore, logBuffer *store.LogBuffer, svcMgr *ServiceManager, startedAt time.Time) *APIHandler {
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	return &APIHandler{
		cfgMgr:     cfgMgr,
		alertStore: alertStore,
		logBuffer:  logBuffer,
		svcMgr:     svcMgr,
		startedAt:  startedAt,
	}
}

// HandleHealthz serves GET /healthz.
func (h *APIHandler) HandleHealthz(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":         "ok",
		"uptime_seconds": int64(time.Since(h.startedAt).Seconds()),
		"version":        appVersion,
	})
}

// HandleReadyz serves GET /readyz.
func (h *APIHandler) HandleReadyz(c *gin.Context) {
	var cfg *config.AppConfig
	if h.cfgMgr != nil {
		cfg = h.cfgMgr.Get()
	}
	clients := []string{}
	if cfg != nil && cfg.Clients != nil {
		clients = append(clients, cfg.Clients...)
	}
	c.JSON(http.StatusOK, gin.H{
		"status":          "ready",
		"config_loaded":   cfg != nil,
		"enabled_clients": clients,
	})
}

// HandleAuthStatus serves GET /api/auth/status (publicly reachable to render login state).
func (h *APIHandler) HandleAuthStatus(c *gin.Context) {
	expected := ""
	if h.cfgMgr != nil {
		expected = h.cfgMgr.Get().Server.AuthToken
	}
	if expected == "" {
		c.JSON(http.StatusOK, gin.H{
			"auth_required": false,
			"authenticated": true,
		})
		return
	}

	provided := extractAPIKey(c)
	authenticated := subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
	c.JSON(http.StatusOK, gin.H{
		"auth_required": true,
		"authenticated": authenticated,
	})
}

// HandleAuthLogin serves POST /api/auth/login to verify the API Key / token before entering the UI.
func (h *APIHandler) HandleAuthLogin(c *gin.Context) {
	var req struct {
		APIKey string `json:"api_key"`
		Token  string `json:"token"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"ok":    false,
			"error": "请求参数格式错误: " + err.Error(),
		})
		return
	}

	expected := ""
	if h.cfgMgr != nil {
		expected = h.cfgMgr.Get().Server.AuthToken
	}
	if expected == "" {
		c.JSON(http.StatusOK, gin.H{
			"ok":            true,
			"auth_required": false,
			"authenticated": true,
		})
		return
	}

	provided := strings.TrimSpace(req.APIKey)
	if provided == "" {
		provided = strings.TrimSpace(req.Token)
	}
	if subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
		c.JSON(http.StatusUnauthorized, gin.H{
			"ok":    false,
			"error": "API Key 无效，请检查后重试",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"ok":            true,
		"auth_required": true,
		"authenticated": true,
	})
}

// HandleOverview serves GET /api/overview.
func (h *APIHandler) HandleOverview(c *gin.Context) {
	cfg := h.cfgMgr.Get()
	loc := utils.GetShanghaiLocation()

	var stats store.OverviewStats
	var recent []store.AlertRecord
	if h.alertStore != nil {
		stats = h.alertStore.Stats(cfg)
		recent = h.alertStore.Recent(8)
	} else {
		recent = []store.AlertRecord{}
	}

	var services ServicesStatus
	if h.svcMgr != nil {
		services = h.svcMgr.Status()
	}

	c.JSON(http.StatusOK, gin.H{
		"version":        appVersion,
		"started_at":     h.startedAt.In(loc).Format(time.RFC3339),
		"uptime_seconds": int64(time.Since(h.startedAt).Seconds()),
		"server_port":    cfg.Server.Port,
		"config_path":    h.cfgMgr.ConfigPath(),
		"auth_required":  cfg.Server.AuthToken != "",
		"setup_complete": cfg.HasConfiguredClient(),
		"stats":          stats,
		"services":       services,
		"recent_alerts":  recent,
	})
}

// HandleListAlerts serves GET /api/alerts.
func (h *APIHandler) HandleListAlerts(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	result := h.alertStore.Query(store.AlertQuery{
		Page:          page,
		PageSize:      pageSize,
		Status:        c.Query("status"),
		DeliveryState: c.Query("delivery_state"),
		Source:        c.Query("source"),
		Severity:      c.Query("severity"),
		Q:             c.Query("q"),
	})
	c.JSON(http.StatusOK, result)
}

// HandleClearAlerts serves DELETE /api/alerts.
func (h *APIHandler) HandleClearAlerts(c *gin.Context) {
	if h.alertStore != nil {
		_ = h.alertStore.Clear()
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// HandleGetConfig serves GET /api/config.
func (h *APIHandler) HandleGetConfig(c *gin.Context) {
	cfg := h.cfgMgr.Get()
	warnings := h.cfgMgr.Validate(cfg)
	if warnings == nil {
		warnings = []config.ValidationIssue{}
	}
	envOverrides := h.cfgMgr.EnvOverrides()
	if envOverrides == nil {
		envOverrides = []string{}
	}

	c.JSON(http.StatusOK, gin.H{
		"config":        cfg,
		"config_path":   h.cfgMgr.ConfigPath(),
		"yaml":          h.cfgMgr.RawYAML(),
		"warnings":      warnings,
		"env_overrides": envOverrides,
	})
}

// HandleUpdateConfig serves PUT /api/config.
func (h *APIHandler) HandleUpdateConfig(c *gin.Context) {
	var req config.AppConfig
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求 JSON 格式错误: " + err.Error()})
		return
	}

	warnings, restartRequired, err := h.cfgMgr.Update(&req)
	if err != nil {
		h.writeConfigError(c, err)
		return
	}
	if warnings == nil {
		warnings = []config.ValidationIssue{}
	}

	c.JSON(http.StatusOK, gin.H{
		"ok":               true,
		"config":           h.cfgMgr.Get(),
		"yaml":             h.cfgMgr.RawYAML(),
		"warnings":         warnings,
		"restart_required": restartRequired,
	})
}

// HandleUpdateConfigYAML serves PUT /api/config/yaml.
func (h *APIHandler) HandleUpdateConfigYAML(c *gin.Context) {
	var req struct {
		YAML string `json:"yaml"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求 JSON 格式错误: " + err.Error()})
		return
	}

	warnings, restartRequired, err := h.cfgMgr.UpdateYAML(req.YAML)
	if err != nil {
		h.writeConfigError(c, err)
		return
	}
	if warnings == nil {
		warnings = []config.ValidationIssue{}
	}

	c.JSON(http.StatusOK, gin.H{
		"ok":               true,
		"config":           h.cfgMgr.Get(),
		"yaml":             h.cfgMgr.RawYAML(),
		"warnings":         warnings,
		"restart_required": restartRequired,
	})
}

// HandleReloadConfig serves POST /api/config/reload.
func (h *APIHandler) HandleReloadConfig(c *gin.Context) {
	warnings, err := h.cfgMgr.Reload()
	if err != nil {
		h.writeConfigError(c, err)
		return
	}
	if warnings == nil {
		warnings = []config.ValidationIssue{}
	}

	c.JSON(http.StatusOK, gin.H{
		"ok":       true,
		"config":   h.cfgMgr.Get(),
		"yaml":     h.cfgMgr.RawYAML(),
		"warnings": warnings,
	})
}

func (h *APIHandler) writeConfigError(c *gin.Context, err error) {
	var valErr *config.ValidationError
	if errors.As(err, &valErr) {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":  valErr.Error(),
			"issues": valErr.Issues,
		})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
}

// HandleTestNotifier serves POST /api/test/notifier.
func (h *APIHandler) HandleTestNotifier(c *gin.Context) {
	var req struct {
		Client     string                 `json:"client"`
		WebhookURL string                 `json:"webhook_url"`
		Target     string                 `json:"target"`
		Webhooks   []config.WebhookTarget `json:"webhooks"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数格式错误: " + err.Error()})
		return
	}

	cfg := h.cfgMgr.Get()
	clientName := strings.TrimSpace(strings.ToLower(req.Client))
	if clientName == "" {
		clientName = "all"
	}

	if clientName == "all" {
		uniqueClients := deduplicateClients(cfg.Clients)
		if len(uniqueClients) == 0 {
			c.JSON(http.StatusOK, gin.H{
				"ok":      false,
				"results": []store.DeliveryResult{},
			})
			return
		}

		perClient := make([][]store.DeliveryResult, len(uniqueClients))
		var wg sync.WaitGroup
		for i, ch := range uniqueClients {
			wg.Add(1)
			go func(idx int, client string) {
				defer wg.Done()
				perClient[idx] = TestNotifierTargets(client, cfg.Notifiers[client].EnabledTargets())
			}(i, ch)
		}
		wg.Wait()

		results := make([]store.DeliveryResult, 0, len(uniqueClients))
		allOK := true
		for _, group := range perClient {
			for _, res := range group {
				if !res.Success {
					allOK = false
				}
				results = append(results, res)
			}
		}
		if len(results) == 0 {
			allOK = false
		}
		c.JSON(http.StatusOK, gin.H{
			"ok":      allOK,
			"results": results,
		})
		return
	}

	if !config.IsValidClientName(clientName) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "不支持的通知渠道: " + req.Client})
		return
	}

	var results []store.DeliveryResult
	if strings.TrimSpace(req.WebhookURL) != "" {
		results = []store.DeliveryResult{
			TestNotifierClientWithTarget(clientName, strings.TrimSpace(req.Target), req.WebhookURL),
		}
	} else if len(req.Webhooks) > 0 {
		results = TestNotifierTargets(clientName, req.Webhooks)
	} else {
		results = TestNotifierTargets(clientName, cfg.Notifiers[clientName].EnabledTargets())
	}

	allOK := len(results) > 0
	for _, res := range results {
		if !res.Success {
			allOK = false
			break
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":      allOK,
		"results": results,
	})
}

// HandleTestClickHouse serves POST /api/test/clickhouse.
func (h *APIHandler) HandleTestClickHouse(c *gin.Context) {
	chCfg := h.cfgMgr.Get().ClickHouse

	bodyBytes, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "读取请求体失败"})
		return
	}
	if trimmed := bytes.TrimSpace(bodyBytes); len(trimmed) > 0 && string(trimmed) != "{}" {
		var override config.ClickHouseConfig
		if err := json.Unmarshal(trimmed, &override); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "ClickHouse 配置 JSON 无效: " + err.Error()})
			return
		}
		var rawFields map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &rawFields); err == nil {
			if _, ok := rawFields["host"]; ok {
				chCfg.Host = override.Host
			}
			if _, ok := rawFields["port"]; ok {
				chCfg.Port = override.Port
			}
			if _, ok := rawFields["database"]; ok {
				chCfg.Database = override.Database
			}
			if _, ok := rawFields["username"]; ok {
				chCfg.Username = override.Username
			}
			if _, ok := rawFields["password"]; ok {
				chCfg.Password = override.Password
			}
		}
	}

	res := h.svcMgr.TestClickHouse(chCfg)
	c.JSON(http.StatusOK, res)
}

// HandleTestKafka serves POST /api/test/kafka.
func (h *APIHandler) HandleTestKafka(c *gin.Context) {
	kCfg := h.cfgMgr.Get().KafkaMonitor

	bodyBytes, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "读取请求体失败"})
		return
	}
	if trimmed := bytes.TrimSpace(bodyBytes); len(trimmed) > 0 && string(trimmed) != "{}" {
		var override config.KafkaMonitorConfig
		if err := json.Unmarshal(trimmed, &override); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Kafka 配置 JSON 无效: " + err.Error()})
			return
		}
		var rawFields map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &rawFields); err == nil {
			if _, ok := rawFields["kafka_ui_url"]; ok {
				kCfg.KafkaUIURL = override.KafkaUIURL
			}
			if _, ok := rawFields["cluster_name"]; ok {
				kCfg.ClusterName = override.ClusterName
			}
		}
	}

	res := h.svcMgr.TestKafkaUI(kCfg)
	c.JSON(http.StatusOK, res)
}

// HandleTestFilter serves POST /api/filter/test.
func (h *APIHandler) HandleTestFilter(c *gin.Context) {
	var req struct {
		AlertName string              `json:"alert_name"`
		Severity  string              `json:"severity"`
		Filter    *config.AlertFilter `json:"filter"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数格式错误: " + err.Error()})
		return
	}

	cfg := h.cfgMgr.Get()
	if req.Filter != nil {
		cfg.Filter = *req.Filter
	}
	decision := cfg.EvaluateAlertFilter(req.AlertName, req.Severity)
	c.JSON(http.StatusOK, decision)
}

// HandleSimulate serves POST /api/simulate.
func (h *APIHandler) HandleSimulate(c *gin.Context) {
	var req struct {
		Status      string            `json:"status"`
		AlertName   string            `json:"alert_name"`
		Severity    string            `json:"severity"`
		Instance    string            `json:"instance"`
		Summary     string            `json:"summary"`
		Description string            `json:"description"`
		Labels      map[string]string `json:"labels"`
		Send        bool              `json:"send"`
		Clients     []string          `json:"clients"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数格式错误: " + err.Error()})
		return
	}

	status := strings.TrimSpace(req.Status)
	if status != "resolved" {
		status = "firing"
	}
	alertName := strings.TrimSpace(req.AlertName)
	if alertName == "" {
		alertName = "SimulatedAlert"
	}
	severity := strings.TrimSpace(req.Severity)
	if severity == "" {
		severity = "warning"
	}

	labels := make(template.KV, len(req.Labels)+3)
	for k, v := range req.Labels {
		labels[k] = v
	}
	labels["alertname"] = alertName
	labels["severity"] = severity
	if req.Instance != "" {
		labels["instance"] = req.Instance
	}

	annotations := template.KV{
		"summary":     req.Summary,
		"description": req.Description,
	}

	loc := utils.GetShanghaiLocation()
	now := time.Now().In(loc)
	startsAt := now
	var endsAt time.Time
	if status == "resolved" {
		startsAt = now.Add(-5 * time.Minute)
		endsAt = now
	}

	alert := template.Alert{
		Status:      status,
		Labels:      labels,
		Annotations: annotations,
		StartsAt:    startsAt,
		EndsAt:      endsAt,
	}
	data := template.Data{
		Receiver: "webhook-notifier",
		Status:   status,
		Alerts:   []template.Alert{alert},
	}

	cfg := h.cfgMgr.Get()
	var decision config.FilterDecision
	if strings.EqualFold(severity, "none") {
		decision = config.FilterDecision{
			Allowed: false,
			Reason:  "告警级别为 none（无效告警）",
			Rule:    "builtin:none",
		}
	} else {
		decision = cfg.EvaluateAlertFilter(alertName, severity)
	}

	previews := gin.H{
		"wechat":   utils.AlertFormatWechat(data),
		"dingtalk": utils.AlertFormatDingtalk(data),
		"feishu":   utils.AlertFormatFeishu(data),
		"payload":  data,
	}

	if !req.Send {
		c.JSON(http.StatusOK, gin.H{
			"filter":     decision,
			"previews":   previews,
			"deliveries": []store.DeliveryResult{},
		})
		return
	}

	if !decision.Allowed {
		var stored *store.AlertRecord
		if h.alertStore != nil {
			rec := h.alertStore.Add(BuildAlertRecord("test", data, alert, "filtered", decision.Reason, nil))
			stored = &rec
		}
		c.JSON(http.StatusOK, gin.H{
			"filter":     decision,
			"previews":   previews,
			"deliveries": []store.DeliveryResult{},
			"record":     stored,
		})
		return
	}

	targetClients := req.Clients
	if len(targetClients) == 0 {
		targetClients = cfg.Clients
	}

	var deliveries []store.DeliveryResult
	if len(targetClients) == 0 {
		deliveries = []store.DeliveryResult{{
			Client:  "system",
			Success: false,
			Error:   "未选择或启用任何通知渠道",
		}}
	} else {
		deliveries = SendToClientTargets(data, targetClients, cfg.NotifierTargetsMap())
	}

	deliveryState := ComputeDeliveryState(deliveries)
	var stored *store.AlertRecord
	if h.alertStore != nil {
		rec := h.alertStore.Add(BuildAlertRecord("test", data, alert, deliveryState, "", deliveries))
		stored = &rec
	}

	c.JSON(http.StatusOK, gin.H{
		"filter":     decision,
		"previews":   previews,
		"deliveries": deliveries,
		"record":     stored,
	})
}

// HandleTriggerTrafficCheck serves POST /api/services/traffic/check.
func (h *APIHandler) HandleTriggerTrafficCheck(c *gin.Context) {
	status, err := h.svcMgr.TriggerTrafficCheck()
	resp := gin.H{
		"ok":     err == nil,
		"status": status,
	}
	if err != nil {
		resp["error"] = err.Error()
	}
	c.JSON(http.StatusOK, resp)
}

// HandleTriggerKafkaCheck serves POST /api/services/kafka/check.
func (h *APIHandler) HandleTriggerKafkaCheck(c *gin.Context) {
	status, err := h.svcMgr.TriggerKafkaCheck()
	resp := gin.H{
		"ok":     err == nil,
		"status": status,
	}
	if err != nil {
		resp["error"] = err.Error()
	}
	c.JSON(http.StatusOK, resp)
}

// HandleGetLogs serves GET /api/logs.
func (h *APIHandler) HandleGetLogs(c *gin.Context) {
	level := c.Query("level")
	q := c.Query("q")
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "200"))

	items := []store.LogEntry{}
	total := 0
	if h.logBuffer != nil {
		items, total = h.logBuffer.Query(level, q, limit)
	}
	c.JSON(http.StatusOK, gin.H{
		"items": items,
		"total": total,
	})
}
