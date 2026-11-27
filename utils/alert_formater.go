package utils

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/alertmanager/template"
)

var (
	shanghaiOnce sync.Once
	shanghaiLoc  *time.Location
)

// GetShanghaiLocation safely returns the Asia/Shanghai location, falling back to UTC+8 if tzdata is unavailable.
func GetShanghaiLocation() *time.Location {
	shanghaiOnce.Do(func() {
		loc, err := time.LoadLocation("Asia/Shanghai")
		if err != nil || loc == nil {
			loc = time.FixedZone("CST", 8*3600)
		}
		shanghaiLoc = loc
	})
	return shanghaiLoc
}

const (
	wechatMaxMessageBytes = 4000
	collapseFooterReserve = 260
)

func formatCollapseNotice(total, shown, omitted int) string {
	return fmt.Sprintf("\n> **⚠️ 告警风暴折叠:** 本次共收到 %d 条告警，已展示前 %d 条，其余 %d 条已折叠，请前往 Web 控制台查看完整记录", total, shown, omitted)
}

func formatFeishuAlertEntry(loc *time.Location, status string, index int, alert template.Alert) string {
	var builder strings.Builder
	if index > 0 {
		builder.WriteString("> ---\n")
	}
	if status == "firing" {
		severity := alert.Labels["severity"]
		builder.WriteString(fmt.Sprintf("> **告警名称:** %s\n", alert.Labels["alertname"]))
		builder.WriteString(fmt.Sprintf("> **级别:** %s\n", MapSeverity(severity)))
		builder.WriteString(fmt.Sprintf("> **实例:** %s\n", alert.Labels["instance"]))
		builder.WriteString(fmt.Sprintf("> **摘要:** %s\n", alert.Annotations["summary"]))
		builder.WriteString(fmt.Sprintf("> **描述:** %s\n", alert.Annotations["description"]))
		builder.WriteString(fmt.Sprintf("> **触发时间:** %s\n", alert.StartsAt.In(loc).Format("2006-01-02 15:04:05")))
	} else if status == "resolved" {
		builder.WriteString(fmt.Sprintf("> **告警名称:** %s\n", alert.Labels["alertname"]))
		builder.WriteString(fmt.Sprintf("> **恢复时间:** %s\n", alert.EndsAt.In(loc).Format("2006-01-02 15:04:05")))
	}
	return builder.String()
}

func AlertFormatFeishu(data template.Data) string {
	msg, _ := FormatFeishuCapped(data, 0)
	return msg
}

// FormatFeishuCapped formats alerts for Feishu, truncating excess alerts when maxBytes > 0 and appending a collapse notice.
func FormatFeishuCapped(data template.Data, maxBytes int) (string, int) {
	loc := GetShanghaiLocation()
	var builder strings.Builder

	switch data.Status {
	case "firing":
		builder.WriteString("**🔥 Prometheus告警通知**\n")
		builder.WriteString("请关注告警信息，相关人员请注意\n")
		builder.WriteString("> **状态:** 告警中\n")
	case "resolved":
		builder.WriteString("**✅ Prometheus告警恢复**\n")
		builder.WriteString("> **状态:** 已恢复\n")
	default:
		return "", 0
	}

	return formatCappedAlerts(&builder, data, maxBytes, func(i int, alert template.Alert) string {
		return formatFeishuAlertEntry(loc, data.Status, i, alert)
	})
}

func formatDingtalkAlertEntry(loc *time.Location, status string, index int, alert template.Alert) string {
	var builder strings.Builder
	if index > 0 {
		builder.WriteString("> ---\n")
	}
	if status == "firing" {
		sev := alert.Labels["severity"]
		color := DingTalkMapSeverityColor(sev)
		builder.WriteString(fmt.Sprintf("**状态: <font color=\"%s\">告警中</font>**\n\n", color))
		builder.WriteString(fmt.Sprintf("**告警名称: <font color=\"%s\">%s</font>**\n\n", color, alert.Labels["alertname"]))
		builder.WriteString(fmt.Sprintf("**告警级别: <font color=\"%s\">%s</font>**\n\n", color, MapSeverity(sev)))
		builder.WriteString(fmt.Sprintf("**监控实例:** %s\n\n", alert.Labels["instance"]))
		builder.WriteString(fmt.Sprintf("**告警摘要:** %s\n\n", alert.Annotations["summary"]))
		builder.WriteString(fmt.Sprintf("**触发时间:** %s\n\n", alert.StartsAt.In(loc).Format("2006-01-02 15:04:05")))
		if desc, ok := alert.Annotations["description"]; ok && desc != "" {
			builder.WriteString(fmt.Sprintf("**详细描述:** %s\n\n", desc))
		}
	} else if status == "resolved" {
		builder.WriteString(fmt.Sprintf("**告警名称**: %s\n", alert.Labels["alertname"]))
		builder.WriteString(fmt.Sprintf("**恢复时间**: %s\n\n", alert.EndsAt.In(loc).Format("2006-01-02 15:04:05")))
	}
	return builder.String()
}

func AlertFormatDingtalk(data template.Data) string {
	msg, _ := FormatDingtalkCapped(data, 0)
	return msg
}

// FormatDingtalkCapped formats alerts for DingTalk, truncating excess alerts when maxBytes > 0 and appending a collapse notice.
func FormatDingtalkCapped(data template.Data, maxBytes int) (string, int) {
	loc := GetShanghaiLocation()
	var builder strings.Builder

	switch data.Status {
	case "firing":
		builder.WriteString("### 🔥 Prometheus告警通知\n\n")
		builder.WriteString(">请关注告警信息\n\n")
	case "resolved":
		builder.WriteString("### ✅ Prometheus告警恢复\n\n")
		builder.WriteString("状态: **已恢复**\n\n")
	default:
		return "", 0
	}

	return formatCappedAlerts(&builder, data, maxBytes, func(i int, alert template.Alert) string {
		return formatDingtalkAlertEntry(loc, data.Status, i, alert)
	})
}

func formatCappedAlerts(builder *strings.Builder, data template.Data, maxBytes int, formatEntry func(int, template.Alert) string) (string, int) {
	total := len(data.Alerts)
	if total == 0 {
		return builder.String(), 0
	}

	softLimit := 0
	if maxBytes > 0 {
		softLimit = maxBytes - collapseFooterReserve
		if softLimit < 0 {
			softLimit = 0
		}
	}

	shown := 0
	for i, alert := range data.Alerts {
		entry := formatEntry(i, alert)
		if maxBytes > 0 && shown > 0 {
			nextLen := builder.Len() + len(entry)
			isLast := i == total-1
			if nextLen > softLimit && (!isLast || nextLen > maxBytes) {
				break
			}
		}
		builder.WriteString(entry)
		shown++
	}

	omitted := total - shown
	if omitted > 0 {
		notice := formatCollapseNotice(total, shown, omitted)
		if maxBytes <= 0 || builder.Len()+len(notice) <= maxBytes {
			builder.WriteString(notice)
			builder.WriteByte('\n')
		}
	}
	return builder.String(), omitted
}

func AlertFormatWechat(data template.Data) string {
	var builder strings.Builder
	alertCount := len(data.Alerts)
	loc := GetShanghaiLocation()

	if data.Status == "firing" {
		// 获取最高严重级别的告警来决定标题颜色
		highestSeverity := getHighestSeverity(data.Alerts)
		builder.WriteString(fmt.Sprintf("**🔥 <font size=18 color=\"%s\">Prometheus 告警通知</font>**\n", MapSeverityColor(highestSeverity)))
		builder.WriteString("请关注告警信息，相关人员请注意\n")

		for i, alert := range data.Alerts {
			if alertCount > 1 && i > 0 {
				builder.WriteByte('\n')
			}
			sev := alert.Labels["severity"]
			color := MapSeverityColor(sev)
			builder.WriteString(fmt.Sprintf(">**状态: <font color=\"%s\">告警中</font>**\n", color))
			builder.WriteString(fmt.Sprintf(">**告警名称: <font color=\"%s\">%s</font>**\n", color, alert.Labels["alertname"]))
			builder.WriteString(fmt.Sprintf(">**级别: <font color=\"%s\">%s</font>**\n", color, MapSeverity(sev)))
			builder.WriteString(fmt.Sprintf(">**实例**: <font color=\"black\">%s</font>\n", alert.Labels["instance"]))
			builder.WriteString(fmt.Sprintf(">**摘要**: <font color=\"black\">%s</font>\n", alert.Annotations["summary"]))
			builder.WriteString(fmt.Sprintf(">**描述**: %s\n", alert.Annotations["description"]))
			builder.WriteString(fmt.Sprintf(">**触发时间**: <font color=\"black\">%s</font>\n", alert.StartsAt.In(loc).Format("2006-01-02 15:04:05")))
		}
	} else if data.Status == "resolved" {
		builder.WriteString("**♻ <font size=18 color=\"green\">Prometheus 告警恢复</font>**\n")
		builder.WriteString(">**状态: <font color=\"green\">已恢复</font>**\n")
		for i, alert := range data.Alerts {
			if alertCount > 1 && i > 0 {
				builder.WriteString(">---\n")
			}
			severity := alert.Labels["severity"]
			color := MapSeverityColor(severity)

			builder.WriteString(fmt.Sprintf(">**告警名称: <font color=\"%s\">%s</font>**\n", color, alert.Labels["alertname"]))
			builder.WriteString(fmt.Sprintf(">**恢复时间**: <font color=\"black\">%s</font>\n", alert.EndsAt.In(loc).Format("2006-01-02 15:04:05")))
		}
	}

	return builder.String()
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

func FilterValidAlerts(alerts []template.Alert) []template.Alert {
	valid := make([]template.Alert, 0)
	for _, alert := range alerts {
		if alert.Labels["severity"] != "none" {
			valid = append(valid, alert)
		}
	}
	return valid
}

// SplitWeChatAlerts 将告警按批次分组，确保每批消息不超过企业微信长度限制
// 返回多个 template.Data，每个包含一部分告警
func SplitWeChatAlerts(data template.Data) []template.Data {
	batches, _ := SplitWeChatAlertsCapped(data, 0)
	return batches
}

// SplitWeChatAlertsCapped splits alerts into batches up to maxBatches (unlimited when maxBatches <= 0)
// and returns the batches along with the number of omitted alerts.
func SplitWeChatAlertsCapped(data template.Data, maxBatches int) ([]template.Data, int) {
	var result []template.Data

	// 如果没有告警，直接返回原数据
	if len(data.Alerts) == 0 {
		return []template.Data{data}, 0
	}

	// 先检查单个告警是否会超长
	singleAlert := template.Data{
		Status: data.Status,
		Alerts: []template.Alert{data.Alerts[0]},
	}
	singleMsg := AlertFormatWechat(singleAlert)

	// 如果单个告警就超长，那只能发送单个告警
	if len(singleMsg) > wechatMaxMessageBytes {
		log.Printf("[警告] 单个告警消息长度 %d 字节，超过微信限制，将尝试发送", len(singleMsg))
		limit := len(data.Alerts)
		if maxBatches > 0 && limit > maxBatches {
			limit = maxBatches
		}
		for _, alert := range data.Alerts[:limit] {
			result = append(result, template.Data{
				Status: data.Status,
				Alerts: []template.Alert{alert},
			})
		}
		return result, len(data.Alerts) - limit
	}

	// 动态分组告警
	currentBatch := template.Data{
		Status: data.Status,
		Alerts: []template.Alert{},
	}
	shown := 0

	for _, alert := range data.Alerts {
		// Copy into a temporary slice so testing does not mutate currentBatch.Alerts' backing array if overflow occurs.
		candidate := make([]template.Alert, len(currentBatch.Alerts)+1)
		copy(candidate, currentBatch.Alerts)
		candidate[len(currentBatch.Alerts)] = alert

		testBatch := template.Data{
			Status: data.Status,
			Alerts: candidate,
		}
		testMsg := AlertFormatWechat(testBatch)

		// 如果添加后超长，先保存当前批次，然后开始新批次
		if len(testMsg) > wechatMaxMessageBytes {
			if len(currentBatch.Alerts) > 0 {
				result = append(result, currentBatch)
				shown += len(currentBatch.Alerts)
				if maxBatches > 0 && len(result) >= maxBatches {
					currentBatch.Alerts = nil
					break
				}
			}
			currentBatch = template.Data{
				Status: data.Status,
				Alerts: []template.Alert{alert},
			}
		} else {
			currentBatch.Alerts = candidate
		}
	}

	// 添加最后一个批次
	if len(currentBatch.Alerts) > 0 && (maxBatches <= 0 || len(result) < maxBatches) {
		result = append(result, currentBatch)
		shown += len(currentBatch.Alerts)
	}

	return result, len(data.Alerts) - shown
}

// RenderWeChatMessages renders batched WeChat markdown messages up to maxBatches,
// folding excess alerts with a summary footer inside the last batch when omitted > 0.
func RenderWeChatMessages(data template.Data, maxBatches int) ([]string, int) {
	batches, omitted := SplitWeChatAlertsCapped(data, maxBatches)
	msgs := make([]string, len(batches))
	for i, batch := range batches {
		msgs[i] = AlertFormatWechat(batch)
	}

	totalAlerts := len(data.Alerts)
	if omitted <= 0 || totalAlerts == 0 || len(batches) == 0 {
		return msgs, omitted
	}

	lastIdx := len(batches) - 1
	lastBatch := batches[lastIdx]
	shown := totalAlerts - omitted
	lastMsg := msgs[lastIdx]
	notice := formatCollapseNotice(totalAlerts, shown, omitted)

	if len(lastMsg)+len(notice) > wechatMaxMessageBytes {
		for len(lastBatch.Alerts) > 1 {
			lastBatch.Alerts = lastBatch.Alerts[:len(lastBatch.Alerts)-1]
			shown--
			omitted++
			lastMsg = AlertFormatWechat(lastBatch)
			notice = formatCollapseNotice(totalAlerts, shown, omitted)
			if len(lastMsg)+len(notice) <= wechatMaxMessageBytes {
				break
			}
		}
	}

	if len(lastMsg)+len(notice) <= wechatMaxMessageBytes {
		msgs[lastIdx] = lastMsg + notice
	} else {
		msgs[lastIdx] = lastMsg
	}

	return msgs, omitted
}
