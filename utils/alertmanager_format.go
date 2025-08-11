package utils

import (
	"fmt"
	"github.com/prometheus/alertmanager/template"
	"strings"
)

func AlertFormatFeishu(data template.Data) string {
	var builder strings.Builder
	alertCount := len(data.Alerts)

	if data.Status == "firing" {
		builder.WriteString("**🔥 Prometheus告警通知**\n")
		builder.WriteString("请关注告警信息，相关人员请注意\n")
		builder.WriteString("> **状态:** 告警中\n")
		for i, alert := range data.Alerts {
			if alertCount > 1 && i > 0 {
				builder.WriteString("> ---\n")
			}
			severity := alert.Labels["severity"]
			builder.WriteString(fmt.Sprintf("> **告警名称:** %s\n", alert.Labels["alertname"]))
			builder.WriteString(fmt.Sprintf("> **级别:** %s\n", MapSeverity(severity)))
			builder.WriteString(fmt.Sprintf("> **实例:** %s\n", alert.Labels["instance"]))
			builder.WriteString(fmt.Sprintf("> **摘要:** %s\n", alert.Annotations["summary"]))
			builder.WriteString(fmt.Sprintf("> **描述:** %s\n", alert.Annotations["description"]))
			builder.WriteString(fmt.Sprintf("> **触发时间:** %s\n", alert.StartsAt.Format("2006-01-02 15:04:05")))
		}
	} else if data.Status == "resolved" {
		builder.WriteString("**✅ Prometheus告警恢复**\n")
		builder.WriteString("> **状态:** 已恢复\n")
		for i, alert := range data.Alerts {
			if alertCount > 1 && i > 0 {
				builder.WriteString("> ---\n")
			}
			builder.WriteString(fmt.Sprintf("> **告警名称:** %s\n", alert.Labels["alertname"]))
			builder.WriteString(fmt.Sprintf("> **恢复时间:** %s\n", alert.EndsAt.Format("2006-01-02 15:04:05")))
		}
	}
	return builder.String()
}

func AlertFormatDingtalk(data template.Data) string {
	var builder strings.Builder
	alertCount := len(data.Alerts)

	if data.Status == "firing" {
		builder.WriteString("### 🔥 Prometheus告警通知\n\n")
		builder.WriteString(fmt.Sprintf(">请关注告警信息\n\n"))

		for i, alert := range data.Alerts {
			if alertCount > 1 && i > 0 {
				builder.WriteString("> ---\n")
			}

			builder.WriteString(fmt.Sprintf("**状态: <font color=\"%s\">告警中</font>**\n\n", DingTalkMapSeverityColor(alert.Labels["severity"])))
			builder.WriteString(fmt.Sprintf("**告警名称: <font color=\"%s\">%s</font>**\n\n", DingTalkMapSeverityColor(alert.Labels["severity"]), alert.Labels["alertname"]))
			builder.WriteString(fmt.Sprintf("**告警级别: <font color=\"%s\">%s</font>**\n\n", DingTalkMapSeverityColor(alert.Labels["severity"]), MapSeverity(alert.Labels["severity"])))
			builder.WriteString(fmt.Sprintf("**监控实例:** %s\n\n", alert.Labels["instance"]))
			builder.WriteString(fmt.Sprintf("**告警摘要:** %s\n\n", alert.Annotations["summary"]))
			builder.WriteString(fmt.Sprintf("**触发时间:** %s\n\n", alert.StartsAt.Format("2006-01-02 15:04:05")))

			if desc, ok := alert.Annotations["description"]; ok && desc != "" {
				builder.WriteString(fmt.Sprintf("**详细描述:** %s\n\n", desc))
			}
		}
	} else if data.Status == "resolved" {
		builder.WriteString("### ✅ Prometheus告警恢复\n\n")
		builder.WriteString(fmt.Sprintf("状态: **已恢复**\n\n"))

		for i, alert := range data.Alerts {
			if alertCount > 1 && i > 0 {
				builder.WriteString("> ---\n")
			}

			builder.WriteString(fmt.Sprintf("**告警名称**: %s\n", alert.Labels["alertname"]))
			builder.WriteString(fmt.Sprintf("**恢复时间**: %s\n\n", alert.EndsAt.Format("2006-01-02 15:04:05")))
		}
	}

	return builder.String()
}

func AlertFormatWechat(data template.Data) string {
	var msg string
	alertCount := len(data.Alerts)

	if data.Status == "firing" {
		msg += "**🔥 <font size=18 color=\"red\">Prometheus 告警通知</font>**\n"
		msg += "请关注告警信息，相关人员请注意\n"
		//msg += ">**状态: <font color=\"red\">告警中</font>**\n"

		for i, alert := range data.Alerts {
			if alertCount > 1 && i > 0 {
				msg += ">---\n"
			}
			msg += fmt.Sprintf(">**状态: <font color=\"%s\">告警中</font>**\n", MapSeverityColor(alert.Labels["severity"]))
			msg += fmt.Sprintf(">**告警名称: <font color=\"%s\">%s</font>**\n", MapSeverityColor(alert.Labels["severity"]), alert.Labels["alertname"])
			msg += fmt.Sprintf(">**级别: <font color=\"%s\">%s</font>**\n", MapSeverityColor(alert.Labels["severity"]), MapSeverity(alert.Labels["severity"]))
			msg += fmt.Sprintf(">**实例**: <font color=\"black\">%s</font>\n", alert.Labels["instance"])
			msg += fmt.Sprintf(">**摘要**: <font color=\"black\">%s</font>\n", alert.Annotations["summary"])
			msg += fmt.Sprintf(">**描述**: <font color=\"black\">%s</font>\n", alert.Annotations["description"])
			msg += fmt.Sprintf(">**触发时间**: <font color=\"black\">%s</font>\n", alert.StartsAt.Format("2006-01-02 15:04:05"))
		}
	} else if data.Status == "resolved" {
		msg += "**✅ <font size=18 color=\"green\">Prometheus 告警恢复</font>**\n"
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
		return "red"
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

func DingTalkMapSeverityColor(severity string) string {
	switch severity {
	case "emergency":
		return "#FF0000"
	case "critical":
		return "#FF7F0E"
	case "warning":
		return "#FFD700"
	case "info":
		return "comment"
	default:
		return "black"
	}
}

func FilterValidAlerts(alerts []template.Alert) []template.Alert {
	valid := make([]template.Alert, 0)
	for _, alert := range alerts {
		if alert.Labels["severity"] != "none" {
			valid = append(valid, alert)
		}
	}
	return valid
}
