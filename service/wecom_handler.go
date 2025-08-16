package service

import (
	"alert-webhook/utils"
	"fmt"
	"log"
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"

	"github.com/prometheus/alertmanager/template"
)

func GinAlertHandler(notifiers map[string]string, enabledClients []string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method != http.MethodPost {
			c.String(http.StatusMethodNotAllowed, "仅支持POST请求")
			return
		}

		var data template.Data
		if err := c.ShouldBindJSON(&data); err != nil {
			log.Printf("解析Alertmanager请求失败: %v", err)
			c.String(http.StatusBadRequest, "无效的请求体")
			return
		}

		// 过滤无效告警
		validAlerts := utils.FilterValidAlerts(data.Alerts)
		if len(validAlerts) == 0 {
			log.Println("所有告警的 severity 都为 none，忽略发送")
			c.String(http.StatusOK, "无有效告警，无需发送")
			return
		}
		// 替换原始 data
		data.Alerts = validAlerts

		var wg sync.WaitGroup
		failedClients := make([]string, 0)

		for _, client := range enabledClients {
			webhookURL, ok := notifiers[client]
			if !ok {
				log.Printf("客户端 %s 未配置", client)
				continue
			}

			wg.Add(1)
			go func(client, url string) {
				defer wg.Done()

				// 根据客户端类型格式化消息
				message, err := formatMessageForClient(client, data)
				if err != nil {
					log.Printf("[%s] 格式化消息失败: %v", client, err)
					failedClients = append(failedClients, client)
					return
				}

				// 发送消息
				if err := SendAlert(client, url, message); err != nil {
					log.Printf("[%s] 发送告警失败: %v", client, err)
					failedClients = append(failedClients, client)
				} else {
					log.Printf("[%s] 告警发送成功", client)
				}
			}(client, webhookURL)
		}

		wg.Wait()

		if len(failedClients) > 0 {
			c.String(http.StatusInternalServerError,
				fmt.Sprintf("部分客户端发送失败: %v", failedClients))
		} else {
			c.String(http.StatusOK, "告警已成功发送到所有客户端")
		}
	}
}

func formatMessageForClient(client string, data template.Data) (interface{}, error) {
	switch client {
	case "wechat":
		log.Printf("转换企业微信格式")
		return WeChatMessage{
			MsgType: "markdown",
			Markdown: MarkdownMessage{
				Content: utils.AlertFormatWechat(data),
			},
		}, nil
	case "dingtalk":
		log.Printf("转换钉钉格式")
		return DingTalkMessage{
			MsgType: "markdown",
			Markdown: DingTalkMarkdown{
				Title: "Prometheus告警",
				Text:  utils.AlertFormatDingtalk(data),
			},
		}, nil
	case "feishu":
		log.Printf("转换飞书格式")
		return FeishuMessage{
			MsgType: "text",
			Content: FeishuContent{
				Text: utils.AlertFormatFeishu(data),
			},
		}, nil
	default:
		return nil, fmt.Errorf("未知客户端类型: %s", client)
	}
}
