package service

import (
	"alert-webhook/utils"
	"fmt"
	"github.com/gin-gonic/gin"
	"log"
	"net/http"
	"sync"

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

		// 并发发送到所有客户端
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

// formatMessageForClient 根据客户端类型格式化消息
func formatMessageForClient(client string, data template.Data) (interface{}, error) {
	commonContent := formatToCommonMarkdown(data)

	switch client {
	case "wechat":
		log.Printf("转换企业微信格式")
		commonContent := utils.AlertFormatWechat(data)
		return WeChatMessage{
			MsgType: "markdown",
			Markdown: MarkdownMessage{
				Content: commonContent,
			},
		}, nil
	case "dingtalk":
		log.Printf("转换钉钉格式")
		return DingTalkMessage{
			MsgType: "markdown",
			Markdown: DingTalkMarkdown{
				Title: "Prometheus告警",
				Text:  commonContent,
			},
		}, nil
	case "feishu":
		log.Printf("转换飞书格式")
		return FeishuMessage{
			MsgType: "text",
			Content: FeishuContent{
				Text: commonContent,
			},
		}, nil
	default:
		return nil, fmt.Errorf("未知客户端类型: %s", client)
	}
}

// 生成通用的markdown格式
func formatToCommonMarkdown(data template.Data) string {
	var msg string

	if data.Status == "firing" {
		msg += "# 🚨 Prometheus告警通知\n"
		msg += "**状态**: FIRING\n"
		for _, alert := range data.Alerts {
			msg += "\n---\n"
			msg += fmt.Sprintf("**告警名称**: %s\n", alert.Labels["alertname"])
			msg += fmt.Sprintf("**级别**: %s\n", alert.Labels["severity"])
			msg += fmt.Sprintf("**实例**: %s\n", alert.Labels["instance"])
			msg += fmt.Sprintf("**摘要**: %s\n", alert.Annotations["summary"])
			msg += fmt.Sprintf("**描述**: %s\n", alert.Annotations["description"])
			msg += fmt.Sprintf("**触发时间**: %s\n", alert.StartsAt.Format("2006-01-02 15:04:05"))
		}
	} else if data.Status == "resolved" {
		msg += "# ✅ Prometheus告警恢复通知\n"
		msg += "**状态**: RESOLVED\n"
		for _, alert := range data.Alerts {
			msg += "\n---\n"
			msg += fmt.Sprintf("**告警名称**: %s\n", alert.Labels["alertname"])
			msg += fmt.Sprintf("**恢复时间**: %s\n", alert.EndsAt.Format("2006-01-02 15:04:05"))
		}
	}

	return msg
}
