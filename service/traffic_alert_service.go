package service

import (
	"alert-webhook/config"
	"alert-webhook/utils"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/prometheus/alertmanager/template"
)

// TrafficAlertService 流量告警服务
type TrafficAlertService struct {
	clickhouseService *ClickHouseService
	config           *config.AppConfig
	notifiers        map[string]string
	enabledClients   []string
	stopChan         chan bool
	wg               sync.WaitGroup
}

// NewTrafficAlertService 创建流量告警服务实例
func NewTrafficAlertService(clickhouseService *ClickHouseService, cfg *config.AppConfig, notifiers map[string]string, enabledClients []string) *TrafficAlertService {
	return &TrafficAlertService{
		clickhouseService: clickhouseService,
		config:           cfg,
		notifiers:        notifiers,
		enabledClients:   enabledClients,
		stopChan:         make(chan bool),
	}
}

// Start 启动流量告警服务
func (t *TrafficAlertService) Start() {
	if !t.config.TrafficAlert.Enabled {
		log.Println("大流量告警功能未启用")
		return
	}

	log.Printf("启动大流量告警服务，检查间隔: %d秒", t.config.TrafficAlert.CheckInterval)
	
	t.wg.Add(1)
	go t.monitorTraffic()
}

// Stop 停止流量告警服务
func (t *TrafficAlertService) Stop() {
	if !t.config.TrafficAlert.Enabled {
		return
	}
	
	log.Println("正在停止大流量告警服务...")
	close(t.stopChan)
	t.wg.Wait()
	log.Println("大流量告警服务已停止")
}

// monitorTraffic 监控流量的主循环
func (t *TrafficAlertService) monitorTraffic() {
	defer t.wg.Done()
	
	ticker := time.NewTicker(time.Duration(t.config.TrafficAlert.CheckInterval) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			t.checkAndAlert()
		case <-t.stopChan:
			return
		}
	}
}

// checkAndAlert 检查流量异常并发送告警
func (t *TrafficAlertService) checkAndAlert() {
	log.Println("开始检查大流量异常...")
	
	// 查询流量异常
	trafficStats, err := t.clickhouseService.CheckTrafficAnomalies()
	if err != nil {
		log.Printf("查询流量异常失败: %v", err)
		return
	}

	if len(trafficStats) == 0 {
		log.Println("未发现流量异常")
		return
	}

	log.Printf("发现 %d 个流量异常", len(trafficStats))

	// 为每个异常生成告警
	for _, stat := range trafficStats {
		t.generateAndSendAlert(stat)
	}
}

// generateAndSendAlert 为流量异常生成并发送告警
func (t *TrafficAlertService) generateAndSendAlert(stat TrafficStats) {
	// 获取详细的大请求信息
	largeRequests, err := t.clickhouseService.GetRecentLargeRequests(stat.Domain, stat.TopPath, 5)
	if err != nil {
		log.Printf("获取大请求详情失败: %v", err)
	}

	// 构造告警数据
	alert := t.createTrafficAlert(stat, largeRequests)
	
	// 构造 template.Data
	data := template.Data{
		Status: "firing",
		Alerts: []template.Alert{alert},
	}

	// 发送到所有启用的客户端
	var wg sync.WaitGroup
	for _, client := range t.enabledClients {
		webhookURL, ok := t.notifiers[client]
		if !ok {
			log.Printf("客户端 %s 未配置", client)
			continue
		}

		wg.Add(1)
		go func(client, url string) {
			defer wg.Done()
			t.sendAlert(client, url, data)
		}(client, webhookURL)
	}
	wg.Wait()
}

// createTrafficAlert 创建流量告警对象
func (t *TrafficAlertService) createTrafficAlert(stat TrafficStats, largeRequests []NginxAccessLog) template.Alert {
	now := time.Now()
	
	// 生成告警摘要
	summary := fmt.Sprintf("域名 %s 路径 %s 发现大流量异常", stat.Domain, stat.TopPath)
	
	// 生成详细描述
	description := fmt.Sprintf(
		"在过去 %d 分钟内:\n"+
		"• 总请求数: %d\n"+
		"• 大请求数量: %d (阈值: %s)\n"+
		"• 大响应数量: %d (阈值: %s)\n"+
		"• 平均请求大小: %.2f 字节\n"+
		"• 平均响应大小: %.2f 字节\n"+
		"• 最大请求大小: %d 字节\n"+
		"• 最大响应大小: %d 字节",
		t.config.TrafficAlert.TimeWindow,
		stat.TotalCount,
		stat.LargeRequestCount,
		formatBytes(t.config.TrafficAlert.RequestSizeThreshold),
		stat.LargeResponseCount,
		formatBytes(t.config.TrafficAlert.ResponseSizeThreshold),
		stat.AvgRequestSize,
		stat.AvgResponseSize,
		stat.MaxRequestSize,
		stat.MaxResponseSize,
	)

	// 添加详细的大请求信息
	if len(largeRequests) > 0 {
		description += "\n\n最近的大请求示例:"
		for i, req := range largeRequests {
			if i >= 3 { // 最多显示3个例子
				break
			}
			description += fmt.Sprintf(
				"\n%d. %s %s %s (请求:%d字节, 响应:%d字节, 耗时:%.3fs)",
				i+1,
				req.Timestamp.Format("15:04:05"),
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
			"alertname":  "HighTrafficAlert",
			"severity":   "warning",
			"domain":     stat.Domain,
			"top_path":   stat.TopPath,
			"source":     "clickhouse",
			"alert_type": "traffic",
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
func (t *TrafficAlertService) sendAlert(client, webhookURL string, data template.Data) {
	log.Printf("向 %s 发送大流量告警", client)

	// 企业微信需要特殊处理消息长度限制
	if client == "wechat" {
		// 将告警分批处理
		alertBatches := utils.SplitWeChatAlerts(data)
		log.Printf("[%s] 大流量告警分为 %d 批发送", client, len(alertBatches))

		for i, batchData := range alertBatches {
			message := WeChatMessage{
				MsgType: "markdown",
				Markdown: MarkdownMessage{
					Content: utils.AlertFormatWechat(batchData),
				},
			}

			if err := SendAlert(client, webhookURL, message); err != nil {
				log.Printf("[%s] 第 %d 批大流量告警发送失败: %v", client, i+1, err)
			} else {
				log.Printf("[%s] 第 %d 批大流量告警发送成功", client, i+1)
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
				Title: "大流量告警",
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
		log.Printf("[%s] 大流量告警发送失败: %v", client, err)
	} else {
		log.Printf("[%s] 大流量告警发送成功", client)
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