package config

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"gopkg.in/yaml.v2"
)

// DefaultConfigYAML is written when the target config file does not exist on first boot.
const DefaultConfigYAML = `server:
  port: "0.0.0.0:18082"
  ui_enabled: true
  auth_token: ""
  data_dir: "./data"
  log_dir: "./logs"

# 消息接收客户端，留空时可通过 Web 控制台或环境变量配置
client: []

notifiers:
  wechat:
    webhook_url: ""
  dingtalk:
    webhook_url: ""
  feishu:
    webhook_url: ""

clickhouse:
  host: "localhost"
  port: 9000
  database: "nginxlogs"
  username: "default"
  password: ""

# 大流量告警配置
traffic_alert:
  enabled: false
  check_interval: 300
  request_size_threshold: 1048576
  response_size_threshold: 5242880
  time_window: 10
  count_threshold: 5

# 告警过滤规则配置
filter:
  alert_name:
    include: []
    exclude: []
  severity:
    include: []
    exclude:
      - "info"
      - "none"

# Kafka消费者组监控配置
kafka_monitor:
  enabled: false
  kafka_ui_url: "http://localhost:8090"
  cluster_name: "local-kafka"
  check_interval: 60
  consumer_lag_threshold: 100000
  monitored_groups: []
  ignored_groups: []
`

// ConfigManager provides thread-safe configuration access, atomic persistence,
// environment variable overrides, and hot-reload callbacks.
type ConfigManager struct {
	mu           sync.RWMutex
	path         string
	config       *AppConfig
	rawYAML      string
	envOverrides []string
	callbacks    []func(*AppConfig)
}

// NewConfigManager loads or initializes configuration from path (or ALERT_WEBHOOK_CONFIG / ./config.yaml).
func NewConfigManager(path string) (*ConfigManager, error) {
	resolvedPath := strings.TrimSpace(path)
	if resolvedPath == "" {
		resolvedPath = strings.TrimSpace(os.Getenv("ALERT_WEBHOOK_CONFIG"))
	}
	if resolvedPath == "" {
		resolvedPath = "./config.yaml"
	}

	var rawBytes []byte
	data, err := os.ReadFile(resolvedPath)
	if err != nil {
		if os.IsNotExist(err) {
			rawBytes = []byte(DefaultConfigYAML)
			if writeErr := atomicWriteFile(resolvedPath, rawBytes); writeErr != nil {
				log.Printf("[Warn] 无法创建默认配置文件 %s: %v，将使用内存默认配置启动", resolvedPath, writeErr)
			} else {
				log.Printf("未找到配置文件，已生成默认配置文件: %s", resolvedPath)
			}
		} else {
			return nil, fmt.Errorf("读取配置文件 %s 失败: %w", resolvedPath, err)
		}
	} else {
		rawBytes = data
	}

	cfg := &AppConfig{}
	if err := yaml.Unmarshal(rawBytes, cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件 %s 失败: %w", resolvedPath, err)
	}
	cfg.ApplyDefaults()

	effective := cfg.Clone()
	overrides := applyEnvOverrides(effective)
	effective.ApplyDefaults()

	mgr := &ConfigManager{
		path:         resolvedPath,
		config:       effective,
		rawYAML:      string(rawBytes),
		envOverrides: overrides,
	}
	SyncGlobals(effective)
	return mgr, nil
}

// Get returns a deep copy of the current effective configuration.
func (m *ConfigManager) Get() *AppConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.config.Clone()
}

// ConfigPath returns the resolved configuration file path.
func (m *ConfigManager) ConfigPath() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.path
}

// RawYAML returns the YAML text currently on disk (or default template).
func (m *ConfigManager) RawYAML() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.rawYAML
}

// EnvOverrides returns the list of active ALERT_WEBHOOK_* environment variables overriding config.
func (m *ConfigManager) EnvOverrides() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]string{}, m.envOverrides...)
}

// Validate checks cfg and returns all validation errors and warnings.
func (m *ConfigManager) Validate(cfg *AppConfig) []ValidationIssue {
	return ValidateConfig(cfg)
}

// OnReload registers a callback invoked after Update, UpdateYAML, or Reload succeeds.
func (m *ConfigManager) OnReload(fn func(*AppConfig)) {
	if fn == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callbacks = append(m.callbacks, fn)
}

// Update validates cfg, saves it atomically as YAML, updates effective config, and triggers reload hooks.
func (m *ConfigManager) Update(cfg *AppConfig) ([]ValidationIssue, bool, error) {
	if cfg == nil {
		issues := []ValidationIssue{{Field: "config", Level: "error", Message: "配置对象不能为空"}}
		return issues, false, &ValidationError{Issues: issues}
	}

	normalized := cfg.Clone()
	normalized.ApplyDefaults()

	issues := ValidateConfig(normalized)
	if HasValidationErrors(issues) {
		return issues, false, &ValidationError{Issues: issues}
	}

	yamlBytes, err := yaml.Marshal(normalized)
	if err != nil {
		return nil, false, fmt.Errorf("序列化 YAML 失败: %w", err)
	}

	return m.commitConfig(normalized, string(yamlBytes), issues)
}

// UpdateYAML parses raw YAML, validates it, saves it atomically, and triggers reload hooks.
func (m *ConfigManager) UpdateYAML(raw string) ([]ValidationIssue, bool, error) {
	if strings.TrimSpace(raw) == "" {
		issues := []ValidationIssue{{Field: "yaml", Level: "error", Message: "YAML 内容不能为空"}}
		return issues, false, &ValidationError{Issues: issues}
	}

	var parsed AppConfig
	if err := yaml.Unmarshal([]byte(raw), &parsed); err != nil {
		issues := []ValidationIssue{{Field: "yaml", Level: "error", Message: fmt.Sprintf("YAML 语法解析失败: %v", err)}}
		return issues, false, &ValidationError{Issues: issues}
	}
	parsed.ApplyDefaults()

	issues := ValidateConfig(&parsed)
	if HasValidationErrors(issues) {
		return issues, false, &ValidationError{Issues: issues}
	}

	return m.commitConfig(&parsed, raw, issues)
}

// Reload re-reads the config file from disk, validates it, and updates the live state.
func (m *ConfigManager) Reload() ([]ValidationIssue, error) {
	m.mu.RLock()
	path := m.path
	m.mu.RUnlock()

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}

	var parsed AppConfig
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		issues := []ValidationIssue{{Field: "yaml", Level: "error", Message: fmt.Sprintf("YAML 语法解析失败: %v", err)}}
		return issues, &ValidationError{Issues: issues}
	}
	parsed.ApplyDefaults()

	issues := ValidateConfig(&parsed)
	if HasValidationErrors(issues) {
		return issues, &ValidationError{Issues: issues}
	}

	effective := parsed.Clone()
	overrides := applyEnvOverrides(effective)
	effective.ApplyDefaults()
	effectiveIssues := ValidateConfig(effective)

	m.mu.Lock()
	m.config = effective
	m.rawYAML = string(data)
	m.envOverrides = overrides
	callbacks := append([]func(*AppConfig){}, m.callbacks...)
	m.mu.Unlock()

	SyncGlobals(effective)
	for _, fn := range callbacks {
		fn(effective.Clone())
	}

	return effectiveIssues, nil
}

func (m *ConfigManager) commitConfig(diskCfg *AppConfig, rawYAML string, _ []ValidationIssue) ([]ValidationIssue, bool, error) {
	m.mu.RLock()
	path := m.path
	m.mu.RUnlock()

	if err := atomicWriteFile(path, []byte(rawYAML)); err != nil {
		return nil, false, fmt.Errorf("写入配置文件失败: %w", err)
	}

	effective := diskCfg.Clone()
	overrides := applyEnvOverrides(effective)
	effective.ApplyDefaults()
	effectiveIssues := ValidateConfig(effective)

	m.mu.Lock()
	oldCfg := m.config
	restartRequired := oldCfg != nil && (oldCfg.Server.Port != effective.Server.Port ||
		oldCfg.Server.DataDir != effective.Server.DataDir ||
		oldCfg.Server.LogDir != effective.Server.LogDir)
	m.config = effective
	m.rawYAML = rawYAML
	m.envOverrides = overrides
	callbacks := append([]func(*AppConfig){}, m.callbacks...)
	m.mu.Unlock()

	SyncGlobals(effective)
	for _, fn := range callbacks {
		fn(effective.Clone())
	}

	return effectiveIssues, restartRequired, nil
}

// applyEnvOverrides applies supported ALERT_WEBHOOK_* environment variables onto cfg.
// For notifier URLs (WECHAT/DINGTALK/FEISHU), the channel is automatically added to cfg.Clients
// ONLY when cfg.Clients was initially empty (not explicitly populated in config.yaml).
func applyEnvOverrides(cfg *AppConfig) []string {
	if cfg == nil {
		return []string{}
	}
	cfg.ApplyDefaults()
	overrides := make([]string, 0)

	if v := strings.TrimSpace(os.Getenv("ALERT_WEBHOOK_CONFIG")); v != "" {
		overrides = append(overrides, "ALERT_WEBHOOK_CONFIG")
	}
	if v := strings.TrimSpace(os.Getenv("ALERT_WEBHOOK_PORT")); v != "" {
		cfg.Server.Port = v
		overrides = append(overrides, "ALERT_WEBHOOK_PORT")
	}
	if v := strings.TrimSpace(os.Getenv("ALERT_WEBHOOK_AUTH_TOKEN")); v != "" {
		cfg.Server.AuthToken = v
		overrides = append(overrides, "ALERT_WEBHOOK_AUTH_TOKEN")
	}
	if v := strings.TrimSpace(os.Getenv("ALERT_WEBHOOK_DATA_DIR")); v != "" {
		cfg.Server.DataDir = v
		overrides = append(overrides, "ALERT_WEBHOOK_DATA_DIR")
	}
	if v := strings.TrimSpace(os.Getenv("ALERT_WEBHOOK_LOG_DIR")); v != "" {
		cfg.Server.LogDir = v
		overrides = append(overrides, "ALERT_WEBHOOK_LOG_DIR")
	}

	// Only auto-populate cfg.Clients from env URLs if client list was not explicitly set.
	clientsWereEmpty := len(cfg.Clients) == 0
	notifierEnvMap := []struct {
		envVar string
		client string
	}{
		{"ALERT_WEBHOOK_WECHAT_URL", "wechat"},
		{"ALERT_WEBHOOK_DINGTALK_URL", "dingtalk"},
		{"ALERT_WEBHOOK_FEISHU_URL", "feishu"},
	}
	for _, item := range notifierEnvMap {
		if v := strings.TrimSpace(os.Getenv(item.envVar)); v != "" {
			cfg.Notifiers[item.client] = NotifierConfig{WebhookURL: v}
			overrides = append(overrides, item.envVar)
			if clientsWereEmpty && !slices.Contains(cfg.Clients, item.client) {
				cfg.Clients = append(cfg.Clients, item.client)
			}
		}
	}

	if v := strings.TrimSpace(os.Getenv("ALERT_WEBHOOK_CLICKHOUSE_HOST")); v != "" {
		cfg.ClickHouse.Host = v
		overrides = append(overrides, "ALERT_WEBHOOK_CLICKHOUSE_HOST")
	}
	if v := strings.TrimSpace(os.Getenv("ALERT_WEBHOOK_CLICKHOUSE_PORT")); v != "" {
		if port, err := strconv.Atoi(v); err == nil && port > 0 && port <= 65535 {
			cfg.ClickHouse.Port = port
			overrides = append(overrides, "ALERT_WEBHOOK_CLICKHOUSE_PORT")
		} else {
			log.Printf("[Warn] 忽略无效的环境变量 ALERT_WEBHOOK_CLICKHOUSE_PORT=%q", v)
		}
	}
	if v := strings.TrimSpace(os.Getenv("ALERT_WEBHOOK_CLICKHOUSE_DB")); v != "" {
		cfg.ClickHouse.Database = v
		overrides = append(overrides, "ALERT_WEBHOOK_CLICKHOUSE_DB")
	}
	if v := strings.TrimSpace(os.Getenv("ALERT_WEBHOOK_CLICKHOUSE_USER")); v != "" {
		cfg.ClickHouse.Username = v
		overrides = append(overrides, "ALERT_WEBHOOK_CLICKHOUSE_USER")
	}
	if v, ok := os.LookupEnv("ALERT_WEBHOOK_CLICKHOUSE_PASSWORD"); ok && v != "" {
		cfg.ClickHouse.Password = v
		overrides = append(overrides, "ALERT_WEBHOOK_CLICKHOUSE_PASSWORD")
	}
	if v := strings.TrimSpace(os.Getenv("ALERT_WEBHOOK_TRAFFIC_ENABLED")); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.TrafficAlert.Enabled = b
			overrides = append(overrides, "ALERT_WEBHOOK_TRAFFIC_ENABLED")
		} else {
			log.Printf("[Warn] 忽略无效的环境变量 ALERT_WEBHOOK_TRAFFIC_ENABLED=%q", v)
		}
	}
	if v := strings.TrimSpace(os.Getenv("ALERT_WEBHOOK_KAFKA_ENABLED")); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.KafkaMonitor.Enabled = b
			overrides = append(overrides, "ALERT_WEBHOOK_KAFKA_ENABLED")
		} else {
			log.Printf("[Warn] 忽略无效的环境变量 ALERT_WEBHOOK_KAFKA_ENABLED=%q", v)
		}
	}
	if v := strings.TrimSpace(os.Getenv("ALERT_WEBHOOK_KAFKA_UI_URL")); v != "" {
		cfg.KafkaMonitor.KafkaUIURL = v
		overrides = append(overrides, "ALERT_WEBHOOK_KAFKA_UI_URL")
	}
	if v := strings.TrimSpace(os.Getenv("ALERT_WEBHOOK_KAFKA_CLUSTER")); v != "" {
		cfg.KafkaMonitor.ClusterName = v
		overrides = append(overrides, "ALERT_WEBHOOK_KAFKA_CLUSTER")
	}

	return overrides
}

// atomicWriteFile writes data via a temporary file + rename, falling back to os.WriteFile on EXDEV/EBUSY.
func atomicWriteFile(targetPath string, data []byte) error {
	dir := filepath.Dir(targetPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	tmpFile, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return os.WriteFile(targetPath, data, 0644)
	}
	tmpPath := tmpFile.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0644); err != nil {
		return err
	}

	if err := os.Rename(tmpPath, targetPath); err != nil {
		if errors.Is(err, syscall.EXDEV) || errors.Is(err, syscall.EBUSY) {
			return os.WriteFile(targetPath, data, 0644)
		}
		return err
	}
	cleanup = false
	return nil
}
