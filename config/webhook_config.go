package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v2"
)

// AlertFilter 告警过滤规则配置
type AlertFilter struct {
	// 基于告警名称的过滤规则
	AlertName AlertNameFilter `yaml:"alert_name"`
	// 基于告警级别的过滤规则
	Severity SeverityFilter `yaml:"severity"`
}

// ClickHouseConfig ClickHouse数据库配置
type ClickHouseConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Database string `yaml:"database"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

// TrafficAlertConfig 大流量告警配置
type TrafficAlertConfig struct {
	// 是否启用大流量告警
	Enabled bool `yaml:"enabled"`
	// 检查间隔（秒）
	CheckInterval int `yaml:"check_interval"`
	// 请求大小阈值（字节）
	RequestSizeThreshold int64 `yaml:"request_size_threshold"`
	// 响应大小阈值（字节）
	ResponseSizeThreshold int64 `yaml:"response_size_threshold"`
	// 时间窗口（分钟）
	TimeWindow int `yaml:"time_window"`
	// 触发告警的请求数量阈值
	CountThreshold int `yaml:"count_threshold"`
}

// AlertNameFilter 告警名称过滤规则
type AlertNameFilter struct {
	// 包含规则：只有在此列表中的告警名称才会被转发
	Include []string `yaml:"include"`
	// 排除规则：在此列表中的告警名称不会被转发
	Exclude []string `yaml:"exclude"`
}

// SeverityFilter 告警级别过滤规则
type SeverityFilter struct {
	// 包含规则：只有在此列表中的告警级别才会被转发
	Include []string `yaml:"include"`
	// 排除规则：在此列表中的告警级别不会被转发
	Exclude []string `yaml:"exclude"`
}

type NotifierConfig struct {
	WebhookURL string `yaml:"webhook_url"`
}

type ServerConfig struct {
	Port string `yaml:"port"`
}

type AppConfig struct {
	Clients      []string                  `yaml:"client"`
	Notifiers    map[string]NotifierConfig `yaml:"notifiers"`
	Server       ServerConfig              `yaml:"server"`
	// 告警过滤规则
	Filter       AlertFilter               `yaml:"filter"`
	// ClickHouse配置
	ClickHouse   ClickHouseConfig          `yaml:"clickhouse"`
	// 大流量告警配置
	TrafficAlert TrafficAlertConfig        `yaml:"traffic_alert"`
}

// LoadConfig 根据传入配置文件的路径 --- 加载配置
func LoadConfig(path string) (*AppConfig, error) {
	config := &AppConfig{}

	file, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}

	err = yaml.Unmarshal(file, config)
	if err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}

	// 验证配置
	if len(config.Clients) == 0 {
		return nil, fmt.Errorf("未配置任何客户端")
	}

	for _, client := range config.Clients {
		if _, ok := config.Notifiers[client]; !ok {
			return nil, fmt.Errorf("客户端 %s 的配置缺失", client)
		}
		if config.Notifiers[client].WebhookURL == "" {
			return nil, fmt.Errorf("客户端 %s 的Webhook URL未配置", client)
		}
	}

	return config, nil
}

// ShouldSendAlert 根据过滤规则判断是否应该发送告警
func (c *AppConfig) ShouldSendAlert(alertName, severity string) bool {
	// 检查告警名称过滤规则
	if !c.checkAlertNameFilter(alertName) {
		return false
	}

	// 检查告警级别过滤规则
	if !c.checkSeverityFilter(severity) {
		return false
	}

	return true
}

// checkAlertNameFilter 检查告警名称过滤规则
func (c *AppConfig) checkAlertNameFilter(alertName string) bool {
	filter := c.Filter.AlertName

	// 如果没有配置任何过滤规则，默认通过
	if len(filter.Include) == 0 && len(filter.Exclude) == 0 {
		return true
	}

	// 检查排除规则（优先级高）
	for _, excludePattern := range filter.Exclude {
		if matchesPattern(alertName, excludePattern) {
			return false
		}
	}

	// 如果没有include规则，且没有被exclude，则通过
	if len(filter.Include) == 0 {
		return true
	}

	// 检查包含规则
	for _, includePattern := range filter.Include {
		if matchesPattern(alertName, includePattern) {
			return true
		}
	}

	// 有include规则但不匹配，则不通过
	return false
}

// checkSeverityFilter 检查告警级别过滤规则
func (c *AppConfig) checkSeverityFilter(severity string) bool {
	filter := c.Filter.Severity

	// 如果没有配置任何过滤规则，默认通过
	if len(filter.Include) == 0 && len(filter.Exclude) == 0 {
		return true
	}

	// 检查排除规则（优先级高）
	for _, excludePattern := range filter.Exclude {
		if matchesPattern(severity, excludePattern) {
			return false
		}
	}

	// 如果没有include规则，且没有被exclude，则通过
	if len(filter.Include) == 0 {
		return true
	}

	// 检查包含规则
	for _, includePattern := range filter.Include {
		if matchesPattern(severity, includePattern) {
			return true
		}
	}

	// 有include规则但不匹配，则不通过
	return false
}

// 匹配模式，支持精确匹配和通配符
func matchesPattern(value, pattern string) bool {
	// 精确匹配
	if value == pattern {
		return true
	}

	// 通配符匹配（简单的*通配符支持）
	if strings.Contains(pattern, "*") {
		return matchWildcard(value, pattern)
	}

	return false
}

// 简单的通配符匹配实现
func matchWildcard(value, pattern string) bool {
	// 如果模式只有*，匹配任何值
	if pattern == "*" {
		return true
	}

	// 处理前缀匹配：prefix*
	if strings.HasSuffix(pattern, "*") && !strings.Contains(pattern[:len(pattern)-1], "*") {
		prefix := pattern[:len(pattern)-1]
		return strings.HasPrefix(value, prefix)
	}

	// 处理后缀匹配：*suffix
	if strings.HasPrefix(pattern, "*") && !strings.Contains(pattern[1:], "*") {
		suffix := pattern[1:]
		return strings.HasSuffix(value, suffix)
	}

	// 处理包含匹配：*middle*
	if strings.HasPrefix(pattern, "*") && strings.HasSuffix(pattern, "*") {
		middle := pattern[1 : len(pattern)-1]
		if middle == "" {
			return true // *情况
		}
		return strings.Contains(value, middle)
	}

	// 其他复杂情况的通配符匹配（简化处理）
	parts := strings.Split(pattern, "*")
	if len(parts) <= 1 {
		return value == pattern
	}

	currentPos := 0
	for i, part := range parts {
		if part == "" {
			continue
		}

		pos := strings.Index(value[currentPos:], part)
		if pos == -1 {
			return false
		}

		// 第一部分必须从开头匹配
		if i == 0 && pos != 0 {
			return false
		}

		currentPos += pos + len(part)
	}

	// 最后一部分必须在末尾
	lastPart := parts[len(parts)-1]
	if lastPart != "" && !strings.HasSuffix(value, lastPart) {
		return false
	}

	return true
}
