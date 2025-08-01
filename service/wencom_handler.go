package service

import (
	"fmt"
	"github.com/gin-gonic/gin"
	"log"
	"net/http"

	"github.com/prometheus/alertmanager/template"
)

func GinAlertHandler(webhookURL string) gin.HandlerFunc {
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

		// 转换告警格式
		message := formatToWeChatMarkdown(data)
		log.Printf("转换后的告警消息: %s", message)

		// 发送到企微
		if err := WecomSendAlert(webhookURL, message); err != nil {
			log.Printf("发送到企微失败: %v", err)
			c.String(http.StatusInternalServerError, "发送到企微失败")
			return
		}

		c.String(http.StatusOK, "告警已成功发送到企微")
	}
}

func formatToWeChatMarkdown(data template.Data) string {
	var msg string

	if data.Status == "firing" {
		msg += "# 🚨 **Prometheus 告警通知**\n"
		msg += "**状态**: <font color=\"warning\">FIRING</font>\n"
		for _, alert := range data.Alerts {
			msg += "\n---\n"
			msg += fmt.Sprintf(" **告警名称**: <font color=\"comment\">%s</font>\n", alert.Labels["alertname"])
			msg += fmt.Sprintf(" **级别**: <font color=\"warning\">%s</font>\n", alert.Labels["severity"])
			msg += fmt.Sprintf(" **实例**: %s\n", alert.Labels["instance"])
			msg += fmt.Sprintf(" **摘要**: %s\n", alert.Annotations["summary"])
			msg += fmt.Sprintf(" **描述**: %s\n", alert.Annotations["description"])
			msg += fmt.Sprintf(" **触发时间**: %s\n", alert.StartsAt.Format("2006-01-02 15:04:05"))
		}
	} else if data.Status == "resolved" {
		msg += "# ✅ **Prometheus 告警恢复通知**\n"
		msg += "**状态**: <font color=\"info\">RESOLVED</font>\n"
		for _, alert := range data.Alerts {
			msg += "\n---\n"
			msg += fmt.Sprintf(" **告警名称**: <font color=\"comment\">%s</font>\n", alert.Labels["alertname"])
			msg += fmt.Sprintf(" **恢复时间**: %s\n", alert.EndsAt.Format("2006-01-02 15:04:05"))
		}
	}

	return msg
}
