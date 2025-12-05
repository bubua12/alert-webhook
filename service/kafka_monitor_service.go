package service

import (
	"alert-webhook/config"
	"alert-webhook/utils"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
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
	GroupID      string `json:"groupId"`
	Members      int    `json:"members"`
	Topics       int    `json:"topics"`
	Simple       bool   `json:"simple"`
	State        string `json:"state"`
	ConsumerLag  int64  `json:"consumerLag"`
	Coordinator  Coordinator `json:"coordinator"`
}

// Coordinator 协调器信息
type Coordinator struct {
	ID   int    `json:"id"`
	Host string `json:"host"`
	Port int    `json:"port"`
}

// KafkaMonitorService Kafka监控服务
type KafkaMonitorService struct {
	config         *config.AppConfig
	notifiers      map[string]string
	enabledClients []string
	stopChan       chan bool
	wg             sync.WaitGroup
	httpClient     *http.Client
}

// NewKafkaMonitorService 创建Kafka监控服务实例
func NewKafkaMonitorService(cfg *config.AppConfig, notifiers map[string]string, enabledClients []string) *KafkaMonitorService {
	return &KafkaMonitorService{
		config:         cfg,
		notifiers:      notifiers,
		enabledClients: enabledClients,
		stopChan:       make(chan bool),
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// Start 启动Kafka监控服务
func (k *KafkaMonitorService) Start() {
	if !k.config.KafkaMonitor.Enabled {
		log.Println("Kafka监控功能未启用")
		return
	}

	log.Printf("启动Kafka监控服务，检查间隔: %d秒", k.config.KafkaMonitor.CheckInterval)

	k.wg.Add(1)
	go k.monitorKafka()
}

// Stop 停止Kafka监控服务
func (k *KafkaMonitorService) Stop() {
	if !k.config.KafkaMonitor.Enabled {
		return
	}

	log.Println("正在停止Kafka监控服务...")
	close(k.stopChan)
	k.wg.Wait()
	log.Println("Kafka监控服务已停止")
}

// monitorKafka 监控Kafka的主循环
func (k *KafkaMonitorService) monitorKafka() {
	defer k.wg.Done()

	ticker := time.NewTicker(time.Duration(k.config.KafkaMonitor.CheckInterval) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			k.checkAndAlert()
		case <-k.stopChan:
			return
		}
	}
}

// checkAndAlert 检查Kafka消费者组积压并发送告警
func (k *KafkaMonitorService) checkAndAlert() {
	log.Println("开始检查Kafka消费者组积压...")

	// 获取消费者组信息
	consumerGroups, err := k.fetchConsumerGroups()
	if err != nil {
		log.Printf("获取Kafka消费者组信息失败: %v", err)
		return
	}

	if len(consumerGroups) == 0 {
		log.Println("未发现任何消费者组")
		return
	}

	// 检查每个消费者组的积压情况
	var alertGroups []ConsumerGroupDetail
	for _, group := range consumerGroups {
		// 跳过忽略的消费者组
		if k.shouldIgnoreGroup(group.GroupID) {
			continue
		}

		// 如果配置了监控列表，只监控列表中的组
		if len(k.config.KafkaMonitor.MonitoredGroups) > 0 && !k.shouldMonitorGroup(group.GroupID) {
			continue
		}

		// 检查是否超过阈值
		if group.ConsumerLag > k.config.KafkaMonitor.ConsumerLagThreshold {
			alertGroups = append(alertGroups, group)
			log.Printf("消费者组 %s 积压告警: %d (阈值: %d)", 
				group.GroupID, group.ConsumerLag, k.config.KafkaMonitor.ConsumerLagThreshold)
		}
	}

	if len(alertGroups) == 0 {
		log.Println("未发现消费者组积压异常")
		return
	}

	log.Printf("发现 %d 个消费者组积压异常", len(alertGroups))

	// 为每个积压的消费者组发送告警
	for _, group := range alertGroups {
		k.generateAndSendAlert(group)
	}
}

// fetchConsumerGroups 从Kafka UI API获取消费者组信息
func (k *KafkaMonitorService) fetchConsumerGroups() ([]ConsumerGroupDetail, error) {
	// 构建API URL
	apiURL := fmt.Sprintf("%s/api/clusters/%s/consumer-groups/paged?page=1&perPage=100&search=",
		k.config.KafkaMonitor.KafkaUIURL,
		k.config.KafkaMonitor.ClusterName)

	resp, err := k.httpClient.Get(apiURL)
	if err != nil {
		return nil, fmt.Errorf("HTTP请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API返回错误状态码: %d, 响应: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}

	var kafkaResp KafkaUIResponse
	if err := json.Unmarshal(body, &kafkaResp); err != nil {
		return nil, fmt.Errorf("解析JSON失败: %w", err)
	}

	return kafkaResp.ConsumerGroups, nil
}

// shouldIgnoreGroup 检查是否应该忽略该消费者组
func (k *KafkaMonitorService) shouldIgnoreGroup(groupID string) bool {
	for _, ignored := range k.config.KafkaMonitor.IgnoredGroups {
		if groupID == ignored {
			return true
		}
	}
	return false
}

// shouldMonitorGroup 检查是否应该监控该消费者组
func (k *KafkaMonitorService) shouldMonitorGroup(groupID string) bool {
	for _, monitored := range k.config.KafkaMonitor.MonitoredGroups {
		if groupID == monitored {
			return true
		}
	}
	return false
}

// generateAndSendAlert 生成并发送告警
func (k *KafkaMonitorService) generateAndSendAlert(group ConsumerGroupDetail) {
	// 构造告警数据
	alert := k.createKafkaAlert(group)

	// 构造 template.Data
	data := template.Data{
		Status: "firing",
		Alerts: []template.Alert{alert},
	}

	// 发送到所有启用的客户端
	var wg sync.WaitGroup
	for _, client := range k.enabledClients {
		webhookURL, ok := k.notifiers[client]
		if !ok {
			log.Printf("客户端 %s 未配置", client)
			continue
		}

		wg.Add(1)
		go func(client, url string) {
			defer wg.Done()
			k.sendAlert(client, url, data)
		}(client, webhookURL)
	}
	wg.Wait()
}

// createKafkaAlert 创建Kafka告警对象
func (k *KafkaMonitorService) createKafkaAlert(group ConsumerGroupDetail) template.Alert {
	now := time.Now()

	// 生成告警摘要
	summary := fmt.Sprintf("Kafka消费者组 %s 积压告警", group.GroupID)

	// 生成详细描述
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
		k.config.KafkaMonitor.ConsumerLagThreshold,
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
		EndsAt:   time.Time{}, // 空时间表示告警仍在进行中
	}
}

// sendAlert 发送告警到指定客户端
func (k *KafkaMonitorService) sendAlert(client, webhookURL string, data template.Data) {
	log.Printf("向 %s 发送Kafka积压告警", client)

	// 企业微信需要特殊处理消息长度限制
	if client == "wechat" {
		// 将告警分批处理
		alertBatches := utils.SplitWeChatAlerts(data)
		log.Printf("[%s] Kafka积压告警分为 %d 批发送", client, len(alertBatches))

		for i, batchData := range alertBatches {
			message := WeChatMessage{
				MsgType: "markdown",
				Markdown: MarkdownMessage{
					Content: utils.AlertFormatWechat(batchData),
				},
			}

			if err := SendAlert(client, webhookURL, message); err != nil {
				log.Printf("[%s] 第 %d 批Kafka积压告警发送失败: %v", client, i+1, err)
			} else {
				log.Printf("[%s] 第 %d 批Kafka积压告警发送成功", client, i+1)
			}

			// 批次之间添加小延迟
			if i < len(alertBatches)-1 {
				time.Sleep(200 * time.Millisecond)
			}
		}
		return
	}

	// 其他客户端正常处理
	var message interface{}

	switch client {
	case "dingtalk":
		message = DingTalkMessage{
			MsgType: "markdown",
			Markdown: DingTalkMarkdown{
				Title: "Kafka消费者组积压告警",
				Text:  utils.AlertFormatDingtalk(data),
			},
		}
	case "feishu":
		message = FeishuMessage{
			MsgType: "text",
			Content: FeishuContent{
				Text: utils.AlertFormatFeishu(data),
			},
		}
	default:
		log.Printf("未知客户端类型: %s", client)
		return
	}

	if err := SendAlert(client, webhookURL, message); err != nil {
		log.Printf("[%s] Kafka积压告警发送失败: %v", client, err)
	} else {
		log.Printf("[%s] Kafka积压告警发送成功", client)
	}
}
