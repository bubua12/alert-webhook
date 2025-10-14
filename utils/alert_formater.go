package utils

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/prometheus/alertmanager/template"
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
	loc, _ := time.LoadLocation("Asia/Shanghai")

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
			builder.WriteString(fmt.Sprintf("**触发时间:** %s\n\n", alert.StartsAt.In(loc).Format("2006-01-02 15:04:05")))

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
			builder.WriteString(fmt.Sprintf("**恢复时间**: %s\n\n", alert.EndsAt.In(loc).Format("2006-01-02 15:04:05")))
		}
	}

	return builder.String()
}

func AlertFormatWechat(data template.Data) string {
	var msg string
	alertCount := len(data.Alerts)
	loc, _ := time.LoadLocation("Asia/Shanghai")

	if data.Status == "firing" {
		// 获取最高严重级别的告警来决定标题颜色
		highestSeverity := getHighestSeverity(data.Alerts)
		msg += fmt.Sprintf("**🔥 <font size=18 color=\"%s\">Prometheus 告警通知</font>**\n", MapSeverityColor(highestSeverity))
		msg += "请关注告警信息，相关人员请注意\n"
		//msg += ">**状态: <font color=\"red\">告警中</font>**\n"

		for i, alert := range data.Alerts {
			if alertCount > 1 && i > 0 {
				msg += "\n"
			}
			msg += fmt.Sprintf(">**状态: <font color=\"%s\">告警中</font>**\n", MapSeverityColor(alert.Labels["severity"]))
			msg += fmt.Sprintf(">**告警名称: <font color=\"%s\">%s</font>**\n", MapSeverityColor(alert.Labels["severity"]), alert.Labels["alertname"])
			msg += fmt.Sprintf(">**级别: <font color=\"%s\">%s</font>**\n", MapSeverityColor(alert.Labels["severity"]), MapSeverity(alert.Labels["severity"]))
			msg += fmt.Sprintf(">**实例**: <font color=\"black\">%s</font>\n", alert.Labels["instance"])
			msg += fmt.Sprintf(">**摘要**: <font color=\"black\">%s</font>\n", alert.Annotations["summary"])
			msg += fmt.Sprintf(">**描述**: %s\n", alert.Annotations["description"])
			msg += fmt.Sprintf(">**触发时间**: <font color=\"black\">%s</font>\n", alert.StartsAt.In(loc).Format("2006-01-02 15:04:05"))
		}
	} else if data.Status == "resolved" {
		msg += "**♻ <font size=18 color=\"green\">Prometheus 告警恢复</font>**\n"
		msg += ">**状态: <font color=\"green\">已恢复</font>**\n"
		for i, alert := range data.Alerts {
			if alertCount > 1 && i > 0 {
				msg += ">---\n"
			}
			severity := alert.Labels["severity"]
			color := MapSeverityColor(severity)

			msg += fmt.Sprintf(">**告警名称: <font color=\"%s\">%s</font>**\n", color, alert.Labels["alertname"])
			msg += fmt.Sprintf(">**恢复时间**: <font color=\"black\">%s</font>\n", alert.EndsAt.In(loc).Format("2006-01-02 15:04:05"))
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

// getHighestSeverity 获取告警列表中的最高严重级别
// 优先级：emergency > critical > warning > info > 其他
func getHighestSeverity(alerts []template.Alert) string {
	if len(alerts) == 0 {
		return "info"
	}

	// 定义严重级别优先级
	severityPriority := map[string]int{
		"emergency": 4,
		"critical":  3,
		"warning":   2,
		"info":      1,
	}

	highestSeverity := "info"
	highestPriority := 0

	for _, alert := range alerts {
		severity := alert.Labels["severity"]
		if priority, exists := severityPriority[severity]; exists {
			if priority > highestPriority {
				highestPriority = priority
				highestSeverity = severity
			}
		} else {
			// 对于未知的严重级别，如果当前没有找到任何已知级别，则使用它
			if highestPriority == 0 {
				highestSeverity = severity
			}
		}
	}

	return highestSeverity
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

func AlertFormatPlainText(data template.Data) string {
	var builder strings.Builder
	alertCount := len(data.Alerts)
	loc, _ := time.LoadLocation("Asia/Shanghai")

	if data.Status == "firing" {
		builder.WriteString("🔥 Prometheus告警通知\n")
		builder.WriteString("请关注告警信息，相关人员请注意\n\n")

		for i, alert := range data.Alerts {
			if alertCount > 1 && i > 0 {
				builder.WriteString("\n---\n\n")
			}

			builder.WriteString(fmt.Sprintf("状态: 告警中\n"))
			builder.WriteString(fmt.Sprintf("告警名称: %s\n", alert.Labels["alertname"]))
			builder.WriteString(fmt.Sprintf("级别: %s\n", MapSeverity(alert.Labels["severity"])))
			builder.WriteString(fmt.Sprintf("实例: %s\n", alert.Labels["instance"]))
			builder.WriteString(fmt.Sprintf("摘要: %s\n", alert.Annotations["summary"]))
			if desc, ok := alert.Annotations["description"]; ok && desc != "" {
				builder.WriteString(fmt.Sprintf("描述: %s\n", desc))
			}
			builder.WriteString(fmt.Sprintf("触发时间: %s\n", alert.StartsAt.In(loc).Format("2006-01-02 15:04:05")))
		}
	} else if data.Status == "resolved" {
		builder.WriteString("✅ Prometheus告警恢复\n")
		builder.WriteString("状态: 已恢复\n\n")

		for i, alert := range data.Alerts {
			if alertCount > 1 && i > 0 {
				builder.WriteString("\n---\n\n")
			}
			builder.WriteString(fmt.Sprintf("告警名称: %s\n", alert.Labels["alertname"]))
			builder.WriteString(fmt.Sprintf("恢复时间: %s\n", alert.EndsAt.In(loc).Format("2006-01-02 15:04:05")))
		}
	}

	return builder.String()
}

// SplitWeChatAlerts 将告警按批次分组，确保每批消息不超过企业微信长度限制
// 返回多个 template.Data，每个包含一部分告警
func SplitWeChatAlerts(data template.Data) []template.Data {
	const maxLength = 4000 // 企业微信限制4096字节，留一些安全边界

	var result []template.Data

	// 如果没有告警，直接返回原数据
	if len(data.Alerts) == 0 {
		return []template.Data{data}
	}

	// 先检查单个告警是否会超长
	singleAlert := template.Data{
		Status: data.Status,
		Alerts: []template.Alert{data.Alerts[0]},
	}
	singleMsg := AlertFormatWechat(singleAlert)

	// 如果单个告警就超长，那只能发送单个告警
	if len(singleMsg) > maxLength {
		log.Printf("[警告] 单个告警消息长度 %d 字节，超过微信限制，将尝试发送", len(singleMsg))
		// 对于超长的单个告警，我们还是尝试发送，让微信返回错误
		for _, alert := range data.Alerts {
			singleData := template.Data{
				Status: data.Status,
				Alerts: []template.Alert{alert},
			}
			result = append(result, singleData)
		}
		return result
	}

	// 动态分组告警
	currentBatch := template.Data{
		Status: data.Status,
		Alerts: []template.Alert{},
	}

	for _, alert := range data.Alerts {
		// 尝试添加当前告警到批次中
		testBatch := template.Data{
			Status: data.Status,
			Alerts: append(currentBatch.Alerts, alert),
		}

		testMsg := AlertFormatWechat(testBatch)

		// 如果添加后超长，先保存当前批次，然后开始新批次
		if len(testMsg) > maxLength {
			if len(currentBatch.Alerts) > 0 {
				result = append(result, currentBatch)
			}
			// 开始新批次
			currentBatch = template.Data{
				Status: data.Status,
				Alerts: []template.Alert{alert},
			}
		} else {
			// 可以添加到当前批次
			currentBatch.Alerts = append(currentBatch.Alerts, alert)
		}
	}

	// 添加最后一个批次
	if len(currentBatch.Alerts) > 0 {
		result = append(result, currentBatch)
	}

	return result
}

// FilterValidAlerts 过滤有效告警
func FilterValidAlerts(alerts []template.Alert) []template.Alert {
	valid := make([]template.Alert, 0)
	for _, alert := range alerts {
		if alert.Labels["severity"] != "none" {
			valid = append(valid, alert)
		}
	}
	return valid
}
