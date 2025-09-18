package config

import (
	"alert-webhook/console"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"
)

var (
	EnabledClients []string
	Notifiers      map[string]string // client -> webhook_url
	ServerPort     string
	GlobalConfig   *AppConfig // 全局配置引用
)

func init() {
	// 加载配置
	configPath := flag.String("config", "./config.yaml", "配置文件路径")
	flag.Parse()

	// 如果配置文件不存在，就写入默认配置
	if _, err := os.Stat(*configPath); os.IsNotExist(err) {
		defaultConfig := `server:
  port: "0.0.0.0:18082"

# 消息接收客户端，支持配置数组同时发送
client:
  - wechat

notifiers:
  wechat:
    webhook_url: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=xxxxxxxxxxxxxxxxxxxxxx"
  dingtalk:
    webhook_url: "https://oapi.dingtalk.com/robot/send?access_token=xxxxxxxxxxxxxxxxxxxxxxxxx"
  feishu:
    webhook_url: "https://open.feishu.cn/open-apis/bot/v2/hook/xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"

clickhouse:
  host: "localhost"
  port: 9000
  database: "nginxlogs"
  username: "default"
  password: ""

# 大流量告警配置
traffic_alert:
  # 是否启用大流量告警
  enabled: true
  # 检查间隔（秒）
  check_interval: 300
  # 请求大小阈值（字节），超过此值视为大请求
  request_size_threshold: 1048576   # 1MB
  # 响应大小阈值（字节），超过此值视为大响应  
  response_size_threshold: 5242880  # 5MB
  # 时间窗口（分钟），检查此时间段内的流量
  time_window: 10
  # 触发告警的大请求/响应数量阈值
  count_threshold: 5

# 告警过滤规则配置（可选）
filter:
  # 基于告警名称的过滤规则
  alert_name:
    # 包含规则：只有匹配这些规则的告警才会被发送（支持通配符*）
    include:
      - "HighCPU*"              # 匹配以HighCPU开头的告警
      - "*Memory*"              # 匹配包含Memory的告警  
      - "DiskSpaceLow"          # 精确匹配
      - "DatabaseConnectionError" # 精确匹配
      # - "*"                   # 匹配所有告警（如果需要允许所有告警名称）
    # 排除规则：匹配这些规则的告警不会被发送（优先级高于include）
    exclude:
      - "*Test*"                # 排除包含Test的告警
      - "DebugAlert"            # 排除调试告警
      - "InfoInhibitor"         # 排除信息抑制告警

  # 基于告警级别的过滤规则  
  severity:
    # 包含规则：只发送这些级别的告警
    include:
      - "emergency"             # 紧急告警
      - "critical"              # 严重告警
      - "warning"               # 警告告警
      # - "info"                 # 信息告警（可选）
    # 排除规则：不发送这些级别的告警
    exclude:
      - "info"                  # 排除信息级别告警
      - "none"                  # 排除none级别

`
		err = os.WriteFile(*configPath, []byte(defaultConfig), 0644)
		if err != nil {
			log.Fatalf("无法创建默认配置文件: %v", err)
		}
		fmt.Printf("未找到配置文件，已生成默认配置文件: %s\n", *configPath)
	}

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("配置初始化失败: %v", err)
	}
	console.Success("[Success]", "配置初始化成功")

	// 保存全局配置引用
	GlobalConfig = cfg
	EnabledClients = cfg.Clients
	ServerPort = cfg.Server.Port

	// 构建通知器映射
	Notifiers = make(map[string]string)
	for client, config := range cfg.Notifiers {
		Notifiers[client] = config.WebhookURL
	}
}

// TestClientsConnection 测试所有启用的客户端连通性
func TestClientsConnection() bool {
	allSuccess := true

	for _, client := range EnabledClients {
		url, ok := Notifiers[client]
		if !ok {
			log.Printf("客户端 %s 未配置", client)
			continue
		}

		var msg map[string]interface{}

		switch client {
		case "wechat":
			msg = map[string]interface{}{
				"msgtype": "markdown",
				"markdown": map[string]string{
					"content": "[测试连接]企业微信",
				},
			}
		case "dingtalk":
			msg = map[string]interface{}{
				"msgtype": "markdown",
				"markdown": map[string]string{
					"title": "钉钉连通性测试",
					"text":  "钉钉连通性测试",
				},
			}
		case "feishu":
			msg = map[string]interface{}{
				"msg_type": "text",
				"content": map[string]string{
					"text": "飞书连通性测试",
				},
			}
		default:
			log.Printf("未知客户端类型: %s", client)
			continue
		}

		if success := sendTestMessage(client, url, msg); success {
			log.Printf("[Connection Test Success] %s 连通性测试成功", client)
		} else {
			log.Printf("[Connection Test Faield] %s 连通性测试失败", client)
			allSuccess = false
		}
	}

	return allSuccess
}

// sendTestMessage 发送测试连接消息
func sendTestMessage(clientType, url string, msg map[string]interface{}) bool {
	jsonData, err := json.Marshal(msg)
	if err != nil {
		log.Printf("[%s] JSON 编码失败: %v", clientType, err)
		return false
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		log.Printf("[%s] 创建请求失败: %v", clientType, err)
		return false
	}
	req.Header.Set("Content-Type", "application/json")

	// 修复：将变量名改为 httpClient 避免冲突
	httpClient := &http.Client{Timeout: 5 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		log.Printf("[%s] 发送请求失败: %v", clientType, err)
		return false
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("[%s] 读取响应失败: %v", clientType, err)
		return false
	}

	// 检查响应状态
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("[%s] 返回错误状态码: %d, 响应: %s", clientType, resp.StatusCode, string(body))
		return false
	}

	return true
}
