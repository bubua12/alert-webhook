package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v2"
)

var validDBNameRegex = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)

// SupportedClients lists all built-in notification channel identifiers.
var SupportedClients = []string{"wechat", "dingtalk", "feishu"}

// AlertFilter 告警过滤规则配置
type AlertFilter struct {
	// 基于告警名称的过滤规则
	AlertName AlertNameFilter `yaml:"alert_name" json:"alert_name"`
	// 基于告警级别的过滤规则
	Severity SeverityFilter `yaml:"severity" json:"severity"`
}

// ClickHouseConfig ClickHouse数据库配置
type ClickHouseConfig struct {
	Host     string `yaml:"host" json:"host"`
	Port     int    `yaml:"port" json:"port"`
	Database string `yaml:"database" json:"database"`
	Username string `yaml:"username" json:"username"`
	Password string `yaml:"password" json:"password"`
}

// TrafficAlertConfig 大流量告警配置
type TrafficAlertConfig struct {
	// 是否启用大流量告警
	Enabled bool `yaml:"enabled" json:"enabled"`
	// 检查间隔（秒）
	CheckInterval int `yaml:"check_interval" json:"check_interval"`
	// 请求大小阈值（字节）
	RequestSizeThreshold int64 `yaml:"request_size_threshold" json:"request_size_threshold"`
	// 响应大小阈值（字节）
	ResponseSizeThreshold int64 `yaml:"response_size_threshold" json:"response_size_threshold"`
	// 时间窗口（分钟）
	TimeWindow int `yaml:"time_window" json:"time_window"`
	// 触发告警的请求数量阈值
	CountThreshold int `yaml:"count_threshold" json:"count_threshold"`
}

// KafkaMonitorConfig Kafka监控配置
type KafkaMonitorConfig struct {
	// 是否启用Kafka监控
	Enabled bool `yaml:"enabled" json:"enabled"`
	// Kafka UI的API地址
	KafkaUIURL string `yaml:"kafka_ui_url" json:"kafka_ui_url"`
	// 集群名称
	ClusterName string `yaml:"cluster_name" json:"cluster_name"`
	// 检查间隔（秒）
	CheckInterval int `yaml:"check_interval" json:"check_interval"`
	// 消费者组积压阈值
	ConsumerLagThreshold int64 `yaml:"consumer_lag_threshold" json:"consumer_lag_threshold"`
	// 需要监控的消费者组（为空则监控所有）
	MonitoredGroups []string `yaml:"monitored_groups" json:"monitored_groups"`
	// 忽略的消费者组
	IgnoredGroups []string `yaml:"ignored_groups" json:"ignored_groups"`
}

// AlertNameFilter 告警名称过滤规则
type AlertNameFilter struct {
	// 包含规则：只有在此列表中的告警名称才会被转发
	Include []string `yaml:"include" json:"include"`
	// 排除规则：在此列表中的告警名称不会被转发
	Exclude []string `yaml:"exclude" json:"exclude"`
}

// SeverityFilter 告警级别过滤规则
type SeverityFilter struct {
	// 包含规则：只有在此列表中的告警级别才会被转发
	Include []string `yaml:"include" json:"include"`
	// 排除规则：在此列表中的告警级别不会被转发
	Exclude []string `yaml:"exclude" json:"exclude"`
}

// NotifierConfig 通知渠道配置
type NotifierConfig struct {
	WebhookURL string `yaml:"webhook_url" json:"webhook_url"`
}

// ServerConfig HTTP服务器与控制台配置
type ServerConfig struct {
	Port      string `yaml:"port" json:"port"`
	UIEnabled *bool  `yaml:"ui_enabled,omitempty" json:"ui_enabled,omitempty"`
	AuthToken string `yaml:"auth_token,omitempty" json:"auth_token,omitempty"`
	DataDir   string `yaml:"data_dir,omitempty" json:"data_dir,omitempty"`
	LogDir    string `yaml:"log_dir,omitempty" json:"log_dir,omitempty"`
}

// IsUIEnabled returns whether the embedded web UI is enabled (defaults to true).
func (s ServerConfig) IsUIEnabled() bool {
	return s.UIEnabled == nil || *s.UIEnabled
}

// AppConfig 应用根配置
type AppConfig struct {
	Clients      []string                  `yaml:"client" json:"client"`
	Notifiers    map[string]NotifierConfig `yaml:"notifiers" json:"notifiers"`
	Server       ServerConfig              `yaml:"server" json:"server"`
	Filter       AlertFilter               `yaml:"filter" json:"filter"`
	ClickHouse   ClickHouseConfig          `yaml:"clickhouse" json:"clickhouse"`
	TrafficAlert TrafficAlertConfig        `yaml:"traffic_alert" json:"traffic_alert"`
	KafkaMonitor KafkaMonitorConfig        `yaml:"kafka_monitor" json:"kafka_monitor"`
}

// FilterDecision describes whether an alert passes filter rules and why.
type FilterDecision struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason"`
	Rule    string `json:"rule,omitempty"`
}

// ValidationIssue represents a config validation error or warning.
type ValidationIssue struct {
	Field   string `json:"field"`
	Level   string `json:"level"` // "error" | "warning"
	Message string `json:"message"`
}

// ValidationError is returned when config validation produces one or more error-level issues.
type ValidationError struct {
	Issues []ValidationIssue
}

func (e *ValidationError) Error() string {
	if len(e.Issues) == 0 {
		return "配置验证失败"
	}
	msgs := make([]string, 0, len(e.Issues))
	for _, issue := range e.Issues {
		if issue.Level == "error" {
			msgs = append(msgs, fmt.Sprintf("%s: %s", issue.Field, issue.Message))
		}
	}
	if len(msgs) == 0 {
		return "配置验证失败"
	}
	return "配置验证失败: " + strings.Join(msgs, "; ")
}

func boolPtr(v bool) *bool {
	return &v
}

// DefaultAppConfig returns a fresh default configuration suitable for zero-config boot.
func DefaultAppConfig() *AppConfig {
	return &AppConfig{
		Clients: []string{},
		Notifiers: map[string]NotifierConfig{
			"wechat":   {WebhookURL: ""},
			"dingtalk": {WebhookURL: ""},
			"feishu":   {WebhookURL: ""},
		},
		Server: ServerConfig{
			Port:      "0.0.0.0:18082",
			UIEnabled: boolPtr(true),
			DataDir:   "./data",
			LogDir:    "./logs",
		},
		Filter: AlertFilter{
			AlertName: AlertNameFilter{
				Include: []string{},
				Exclude: []string{},
			},
			Severity: SeverityFilter{
				Include: []string{},
				Exclude: []string{"info", "none"},
			},
		},
		ClickHouse: ClickHouseConfig{
			Host:     "localhost",
			Port:     9000,
			Database: "nginxlogs",
			Username: "default",
			Password: "",
		},
		TrafficAlert: TrafficAlertConfig{
			Enabled:               false,
			CheckInterval:         300,
			RequestSizeThreshold:  1048576,
			ResponseSizeThreshold: 5242880,
			TimeWindow:            10,
			CountThreshold:        5,
		},
		KafkaMonitor: KafkaMonitorConfig{
			Enabled:              false,
			KafkaUIURL:           "http://localhost:8090",
			ClusterName:          "local-kafka",
			CheckInterval:        60,
			ConsumerLagThreshold: 100000,
			MonitoredGroups:      []string{},
			IgnoredGroups:        []string{},
		},
	}
}

// ApplyDefaults populates missing default values and ensures slices/maps are non-nil.
func (c *AppConfig) ApplyDefaults() {
	if c == nil {
		return
	}
	if c.Clients == nil {
		c.Clients = []string{}
	}
	if c.Notifiers == nil {
		c.Notifiers = map[string]NotifierConfig{}
	}
	for _, name := range SupportedClients {
		if _, ok := c.Notifiers[name]; !ok {
			c.Notifiers[name] = NotifierConfig{}
		}
	}
	if strings.TrimSpace(c.Server.Port) == "" {
		c.Server.Port = "0.0.0.0:18082"
	}
	if c.Server.UIEnabled == nil {
		c.Server.UIEnabled = boolPtr(true)
	}
	if strings.TrimSpace(c.Server.DataDir) == "" {
		c.Server.DataDir = "./data"
	}
	if strings.TrimSpace(c.Server.LogDir) == "" {
		c.Server.LogDir = "./logs"
	}
	if c.Filter.AlertName.Include == nil {
		c.Filter.AlertName.Include = []string{}
	}
	if c.Filter.AlertName.Exclude == nil {
		c.Filter.AlertName.Exclude = []string{}
	}
	if c.Filter.Severity.Include == nil {
		c.Filter.Severity.Include = []string{}
	}
	if c.Filter.Severity.Exclude == nil {
		c.Filter.Severity.Exclude = []string{}
	}
	if c.ClickHouse.Port == 0 {
		c.ClickHouse.Port = 9000
	}
	if strings.TrimSpace(c.ClickHouse.Database) == "" {
		c.ClickHouse.Database = "nginxlogs"
	}
	if c.KafkaMonitor.MonitoredGroups == nil {
		c.KafkaMonitor.MonitoredGroups = []string{}
	}
	if c.KafkaMonitor.IgnoredGroups == nil {
		c.KafkaMonitor.IgnoredGroups = []string{}
	}
}

// Clone returns an isolated deep copy of AppConfig.
func (c *AppConfig) Clone() *AppConfig {
	if c == nil {
		return nil
	}
	copyCfg := *c
	copyCfg.Clients = append([]string{}, c.Clients...)

	copyCfg.Notifiers = make(map[string]NotifierConfig, len(c.Notifiers))
	for k, v := range c.Notifiers {
		copyCfg.Notifiers[k] = v
	}

	if c.Server.UIEnabled != nil {
		copyCfg.Server.UIEnabled = boolPtr(*c.Server.UIEnabled)
	}

	copyCfg.Filter.AlertName.Include = append([]string{}, c.Filter.AlertName.Include...)
	copyCfg.Filter.AlertName.Exclude = append([]string{}, c.Filter.AlertName.Exclude...)
	copyCfg.Filter.Severity.Include = append([]string{}, c.Filter.Severity.Include...)
	copyCfg.Filter.Severity.Exclude = append([]string{}, c.Filter.Severity.Exclude...)

	copyCfg.KafkaMonitor.MonitoredGroups = append([]string{}, c.KafkaMonitor.MonitoredGroups...)
	copyCfg.KafkaMonitor.IgnoredGroups = append([]string{}, c.KafkaMonitor.IgnoredGroups...)

	return &copyCfg
}

// LoadConfig 根据传入配置文件的路径加载配置（允许0个客户端以支持Web UI零配置启动）
func LoadConfig(path string) (*AppConfig, error) {
	cfg := &AppConfig{}

	file, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}

	if err := yaml.Unmarshal(file, cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}

	cfg.ApplyDefaults()
	return cfg, nil
}

// IsValidClientName checks whether a client identifier is supported.
func IsValidClientName(client string) bool {
	for _, c := range SupportedClients {
		if c == client {
			return true
		}
	}
	return false
}

// IsValidClickHouseIdentifier validates ClickHouse database/table identifiers against SQL injection.
func IsValidClickHouseIdentifier(name string) bool {
	return validDBNameRegex.MatchString(name)
}

// IsConfiguredWebhookURL reports whether rawURL is a non-empty, non-placeholder HTTP(S) webhook URL.
func IsConfiguredWebhookURL(rawURL string) bool {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return false
	}
	lower := strings.ToLower(trimmed)
	if strings.Contains(lower, "xxxxxx") {
		return false
	}
	if strings.HasSuffix(lower, "key=") || strings.HasSuffix(lower, "access_token=") || strings.HasSuffix(lower, "/hook/") {
		return false
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return false
	}
	return (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}

// HasConfiguredClient reports whether at least one enabled client has a real webhook URL configured.
func (c *AppConfig) HasConfiguredClient() bool {
	if c == nil {
		return false
	}
	for _, client := range c.Clients {
		if n, ok := c.Notifiers[client]; ok && IsConfiguredWebhookURL(n.WebhookURL) {
			return true
		}
	}
	return false
}

// NotifierURLMap returns a flat copy of client -> webhook_url.
func (c *AppConfig) NotifierURLMap() map[string]string {
	m := make(map[string]string)
	if c == nil {
		return m
	}
	for k, v := range c.Notifiers {
		m[k] = v.WebhookURL
	}
	return m
}

// ValidateConfig checks configuration correctness and returns errors and warnings.
func ValidateConfig(cfg *AppConfig) []ValidationIssue {
	issues := make([]ValidationIssue, 0)
	if cfg == nil {
		issues = append(issues, ValidationIssue{Field: "config", Level: "error", Message: "配置对象不能为空"})
		return issues
	}

	// Validate server.port
	if err := validateListenAddr(cfg.Server.Port); err != nil {
		issues = append(issues, ValidationIssue{Field: "server.port", Level: "error", Message: err.Error()})
	}

	// Validate clients & notifier URLs
	if len(cfg.Clients) == 0 {
		issues = append(issues, ValidationIssue{
			Field:   "client",
			Level:   "warning",
			Message: "尚未启用任何通知渠道，告警将仅记录不会外发",
		})
	}
	seenClients := make(map[string]bool)
	for _, client := range cfg.Clients {
		if !IsValidClientName(client) {
			issues = append(issues, ValidationIssue{
				Field:   "client",
				Level:   "error",
				Message: fmt.Sprintf("不支持的通知渠道: %s（支持 wechat、dingtalk、feishu）", client),
			})
			continue
		}
		if seenClients[client] {
			issues = append(issues, ValidationIssue{
				Field:   "client",
				Level:   "warning",
				Message: fmt.Sprintf("通知渠道 %s 重复配置", client),
			})
			continue
		}
		seenClients[client] = true

		n := cfg.Notifiers[client]
		if !IsConfiguredWebhookURL(n.WebhookURL) {
			trimmed := strings.TrimSpace(n.WebhookURL)
			if trimmed != "" && !strings.Contains(strings.ToLower(trimmed), "xxxxxx") {
				if err := validateHTTPURL(trimmed); err != nil {
					issues = append(issues, ValidationIssue{
						Field:   fmt.Sprintf("notifiers.%s.webhook_url", client),
						Level:   "error",
						Message: fmt.Sprintf("%s Webhook URL 格式无效: %v", client, err),
					})
					continue
				}
			}
			issues = append(issues, ValidationIssue{
				Field:   fmt.Sprintf("notifiers.%s.webhook_url", client),
				Level:   "warning",
				Message: fmt.Sprintf("已启用 %s 但 Webhook URL 尚未配置或仍为占位符", client),
			})
		}
	}

	for name, n := range cfg.Notifiers {
		if trimmed := strings.TrimSpace(n.WebhookURL); trimmed != "" && !strings.Contains(strings.ToLower(trimmed), "xxxxxx") {
			if err := validateHTTPURL(trimmed); err != nil && !seenClients[name] {
				issues = append(issues, ValidationIssue{
					Field:   fmt.Sprintf("notifiers.%s.webhook_url", name),
					Level:   "error",
					Message: fmt.Sprintf("%s Webhook URL 格式无效: %v", name, err),
				})
			}
		}
	}

	// Validate ClickHouse database identifier whenever non-empty or traffic alert enabled
	if cfg.ClickHouse.Database != "" && !IsValidClickHouseIdentifier(cfg.ClickHouse.Database) {
		issues = append(issues, ValidationIssue{
			Field:   "clickhouse.database",
			Level:   "error",
			Message: "ClickHouse 数据库名仅允许字母、数字和下划线 (^[a-zA-Z0-9_]+$)",
		})
	}

	if cfg.TrafficAlert.Enabled {
		if strings.TrimSpace(cfg.ClickHouse.Host) == "" {
			issues = append(issues, ValidationIssue{Field: "clickhouse.host", Level: "error", Message: "启用大流量告警时 ClickHouse host 不能为空"})
		}
		if cfg.ClickHouse.Port <= 0 || cfg.ClickHouse.Port > 65535 {
			issues = append(issues, ValidationIssue{Field: "clickhouse.port", Level: "error", Message: "ClickHouse 端口必须在 1-65535 之间"})
		}
		if !IsValidClickHouseIdentifier(cfg.ClickHouse.Database) {
			issues = append(issues, ValidationIssue{Field: "clickhouse.database", Level: "error", Message: "ClickHouse 数据库名无效"})
		}
		if cfg.TrafficAlert.CheckInterval < 5 {
			issues = append(issues, ValidationIssue{Field: "traffic_alert.check_interval", Level: "warning", Message: "检查间隔小于 5 秒，运行时将自动限制为最小 5 秒"})
		}
		if cfg.TrafficAlert.TimeWindow <= 0 {
			issues = append(issues, ValidationIssue{Field: "traffic_alert.time_window", Level: "error", Message: "时间窗口必须大于 0 分钟"})
		}
		if cfg.TrafficAlert.CountThreshold <= 0 {
			issues = append(issues, ValidationIssue{Field: "traffic_alert.count_threshold", Level: "error", Message: "触发告警的请求数量阈值必须大于 0"})
		}
	}

	if cfg.KafkaMonitor.Enabled {
		if err := validateHTTPURL(strings.TrimSpace(cfg.KafkaMonitor.KafkaUIURL)); err != nil {
			issues = append(issues, ValidationIssue{Field: "kafka_monitor.kafka_ui_url", Level: "error", Message: fmt.Sprintf("Kafka UI URL 无效: %v", err)})
		}
		if strings.TrimSpace(cfg.KafkaMonitor.ClusterName) == "" {
			issues = append(issues, ValidationIssue{Field: "kafka_monitor.cluster_name", Level: "error", Message: "启用 Kafka 监控时集群名称不能为空"})
		}
		if cfg.KafkaMonitor.ConsumerLagThreshold < 0 {
			issues = append(issues, ValidationIssue{Field: "kafka_monitor.consumer_lag_threshold", Level: "error", Message: "消费者积压阈值不能为负数"})
		}
		if cfg.KafkaMonitor.CheckInterval < 5 {
			issues = append(issues, ValidationIssue{Field: "kafka_monitor.check_interval", Level: "warning", Message: "检查间隔小于 5 秒，运行时将自动限制为最小 5 秒"})
		}
	}

	return issues
}

func validateListenAddr(addr string) error {
	trimmed := strings.TrimSpace(addr)
	if trimmed == "" {
		return fmt.Errorf("监听地址不能为空")
	}
	_, portStr, err := net.SplitHostPort(trimmed)
	if err != nil {
		return fmt.Errorf("监听地址格式无效 (%s)，应为 host:port 或 :port", trimmed)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("端口号无效: %s", portStr)
	}
	return nil
}

func validateHTTPURL(raw string) error {
	if raw == "" {
		return fmt.Errorf("URL 不能为空")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("必须为合法的 http 或 https URL")
	}
	return nil
}

// HasValidationErrors reports whether any issue has level "error".
func HasValidationErrors(issues []ValidationIssue) bool {
	for _, issue := range issues {
		if issue.Level == "error" {
			return true
		}
	}
	return false
}

// EvaluateAlertFilter evaluates alertName and severity against filter rules and returns a structured decision.
func (c *AppConfig) EvaluateAlertFilter(alertName, severity string) FilterDecision {
	if c == nil {
		return FilterDecision{Allowed: true, Reason: "未配置过滤规则，默认放行"}
	}

	// 1. 告警名称排除规则（优先级最高）
	for _, pattern := range c.Filter.AlertName.Exclude {
		if matchesPattern(alertName, pattern) {
			return FilterDecision{
				Allowed: false,
				Reason:  fmt.Sprintf("告警名称 %q 匹配排除规则 %q", alertName, pattern),
				Rule:    "alert_name.exclude:" + pattern,
			}
		}
	}

	// 2. 告警名称包含规则
	if len(c.Filter.AlertName.Include) > 0 {
		matched := false
		for _, pattern := range c.Filter.AlertName.Include {
			if matchesPattern(alertName, pattern) {
				matched = true
				break
			}
		}
		if !matched {
			return FilterDecision{
				Allowed: false,
				Reason:  fmt.Sprintf("告警名称 %q 未匹配任何包含规则", alertName),
				Rule:    "alert_name.include",
			}
		}
	}

	// 3. 告警级别排除规则
	for _, pattern := range c.Filter.Severity.Exclude {
		if matchesPattern(severity, pattern) {
			return FilterDecision{
				Allowed: false,
				Reason:  fmt.Sprintf("告警级别 %q 匹配排除规则 %q", severity, pattern),
				Rule:    "severity.exclude:" + pattern,
			}
		}
	}

	// 4. 告警级别包含规则
	if len(c.Filter.Severity.Include) > 0 {
		matched := false
		for _, pattern := range c.Filter.Severity.Include {
			if matchesPattern(severity, pattern) {
				matched = true
				break
			}
		}
		if !matched {
			return FilterDecision{
				Allowed: false,
				Reason:  fmt.Sprintf("告警级别 %q 未匹配任何包含规则", severity),
				Rule:    "severity.include",
			}
		}
	}

	return FilterDecision{
		Allowed: true,
		Reason:  "通过所有过滤规则",
	}
}

// ShouldSendAlert 根据过滤规则判断是否应该发送告警
func (c *AppConfig) ShouldSendAlert(alertName, severity string) bool {
	return c.EvaluateAlertFilter(alertName, severity).Allowed
}

// matchesPattern supports exact match and '*' wildcard matching.
func matchesPattern(value, pattern string) bool {
	if value == pattern {
		return true
	}
	if strings.Contains(pattern, "*") {
		return matchWildcard(value, pattern)
	}
	return false
}

// matchWildcard implements full '*' globbing across UTF-8 runes.
func matchWildcard(value, pattern string) bool {
	v := []rune(value)
	p := []rune(pattern)

	vi, pi := 0, 0
	starIdx, matchIdx := -1, 0

	for vi < len(v) {
		if pi < len(p) && p[pi] == v[vi] {
			vi++
			pi++
		} else if pi < len(p) && p[pi] == '*' {
			starIdx = pi
			matchIdx = vi
			pi++
		} else if starIdx != -1 {
			pi = starIdx + 1
			matchIdx++
			vi = matchIdx
		} else {
			return false
		}
	}

	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}
