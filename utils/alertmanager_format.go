package utils

import (
	"fmt"
	"github.com/prometheus/alertmanager/template"
)

// AlertFormatWechat 企业微信格式转换
func AlertFormatWechat(data template.Data) string {
	var msg string
	alertCount := len(data.Alerts)

	if data.Status == "firing" {
		msg += "**🔥 <font color=\"red\">Prometheus告警通知</font>**\n"
		msg += "请关注告警信息，相关人员请注意\n"
		msg += ">**状态: <font color=\"red\">告警中</font>**\n"
		for i, alert := range data.Alerts {
			if alertCount > 1 && i > 0 {
				msg += ">---\n"
			}
			msg += fmt.Sprintf(">**告警名称: <font color=\"red\">%s</font>**\n", alert.Labels["alertname"])
			msg += fmt.Sprintf(">**级别: <font color=\"%s\">%s</font>**\n", MapSeverityColor(alert.Labels["severity"]), MapSeverity(alert.Labels["severity"]))
			msg += fmt.Sprintf(">**实例**: <font color=\"black\">%s</font>\n", alert.Labels["instance"])
			msg += fmt.Sprintf(">**摘要**: <font color=\"black\">%s</font>\n", alert.Annotations["summary"])
			msg += fmt.Sprintf(">**描述**: <font color=\"black\">%s</font>\n", alert.Annotations["description"])
			msg += fmt.Sprintf(">**触发时间**: <font color=\"black\">%s</font>\n", alert.StartsAt.Format("2006-01-02 15:04:05"))
		}
	} else if data.Status == "resolved" {
		msg += "**✅ <font color=\"green\">Prometheus告警恢复</font>**\n"
		msg += ">状态: <font color=\"green\">已恢复</font>\n"
		for i, alert := range data.Alerts {
			if alertCount > 1 && i > 0 {
				msg += ">---\n"
			}
			severity := alert.Labels["severity"]
			color := MapSeverityColor(severity)

			msg += fmt.Sprintf(">告警名称: <font color=\"%s\">%s</font>\n", color, alert.Labels["alertname"])
			msg += fmt.Sprintf(">恢复时间: <font color=\"comment\">%s</font>\n", alert.EndsAt.Format("2006-01-02 15:04:05"))
		}
	}

	return msg
}

// MapSeverity 映射告警等级为标准内部等级（如 P2/P3/P4）
func MapSeverity(severity string) string {
	switch severity {
	case "emergency":
		return "P0"
	case "critical":
		return "P1"
	case "warning":
		return "P2"
	case "info":
		return "P3"
	default:
		return severity
	}
}

// MapSeverityColor 返回告警等级对应的字体颜色（用于企业微信）
func MapSeverityColor(severity string) string {
	switch severity {
	case "emergency":
		return "re"
	case "critical":
		return "red"
	case "warning":
		return "warning"
	case "info":
		return "comment"
	default:
		return "black"
	}
}
