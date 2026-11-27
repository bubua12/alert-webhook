package service

import (
	"alert-webhook/config"
	"alert-webhook/store"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/alertmanager/template"
)

// GinAlertHandler handles incoming Alertmanager webhook payloads using live config and records every alert.
func GinAlertHandler(cfgMgr *config.ConfigManager, alertStore *store.AlertStore) gin.HandlerFunc {
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

		if len(data.Alerts) == 0 {
			c.String(http.StatusOK, "无有效告警，无需发送")
			return
		}

		cfg := cfgMgr.Get()
		allowedAlerts := make([]template.Alert, 0, len(data.Alerts))

		for _, alert := range data.Alerts {
			alertName := alert.Labels["alertname"]
			severity := alert.Labels["severity"]

			if strings.EqualFold(severity, "none") {
				log.Printf("告警 [%s] 级别为 none，忽略发送", alertName)
				if alertStore != nil {
					alertStore.Add(BuildAlertRecord("alertmanager", data, alert, "filtered", "告警级别为 none（无效告警）", nil))
				}
				continue
			}

			decision := cfg.EvaluateAlertFilter(alertName, severity)
			if decision.Allowed {
				allowedAlerts = append(allowedAlerts, alert)
				log.Printf("告警 [%s] 级别 [%s] 通过过滤规则", alertName, severity)
			} else {
				log.Printf("告警 [%s] 级别 [%s] 被过滤规则拦截: %s", alertName, severity, decision.Reason)
				if alertStore != nil {
					alertStore.Add(BuildAlertRecord("alertmanager", data, alert, "filtered", decision.Reason, nil))
				}
			}
		}

		if len(allowedAlerts) == 0 {
			log.Println("所有告警都被过滤规则拦截，忽略发送")
			c.String(http.StatusOK, "所有告警都被过滤，无需发送")
			return
		}

		data.Alerts = allowedAlerts
		if data.Status == "" {
			data.Status = allowedAlerts[0].Status
			if data.Status == "" {
				data.Status = "firing"
			}
		}

		if len(cfg.Clients) == 0 {
			log.Printf("[Warn] 收到 %d 条告警，但当前未启用任何通知渠道，仅记录历史", len(allowedAlerts))
			noClientDelivery := []store.DeliveryResult{{
				Client:  "system",
				Success: false,
				Error:   "未启用任何通知渠道",
			}}
			if alertStore != nil {
				for _, alert := range allowedAlerts {
					alertStore.Add(BuildAlertRecord("alertmanager", data, alert, "failed", "", noClientDelivery))
				}
			}
			c.String(http.StatusOK, "未配置通知渠道，告警已记录")
			return
		}

		log.Printf("过滤后剩余 %d 个告警将被发送", len(allowedAlerts))
		deliveries := SendToClientTargets(data, cfg.Clients, cfg.NotifierTargetsMap())
		deliveryState := ComputeDeliveryState(deliveries)

		if alertStore != nil {
			for _, alert := range allowedAlerts {
				alertStore.Add(BuildAlertRecord("alertmanager", data, alert, deliveryState, "", deliveries))
			}
		}

		var failedClients []string
		for _, d := range deliveries {
			if !d.Success {
				switch {
				case d.Target != "" && d.Client == "wechat" && d.Batches > 1:
					failedClients = append(failedClients, fmt.Sprintf("%s(%s: %s)", d.Client, d.Target, d.Error))
				case d.Target != "":
					failedClients = append(failedClients, fmt.Sprintf("%s(%s)", d.Client, d.Target))
				case d.Client == "wechat" && d.Batches > 1:
					failedClients = append(failedClients, fmt.Sprintf("%s(%s)", d.Client, d.Error))
				default:
					failedClients = append(failedClients, d.Client)
				}
			}
		}

		if len(failedClients) > 0 {
			c.String(http.StatusInternalServerError, fmt.Sprintf("部分客户端发送失败: %v", failedClients))
			return
		}

		c.String(http.StatusOK, "告警已成功发送到所有客户端")
	}
}
