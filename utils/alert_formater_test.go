package utils

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/alertmanager/template"
)

func TestGetShanghaiLocation(t *testing.T) {
	loc := GetShanghaiLocation()
	if loc == nil {
		t.Fatal("expected non-nil Shanghai location")
	}
	utcTime := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	shTime := utcTime.In(loc)
	if shTime.Hour() != 8 {
		t.Fatalf("expected 08:00 in Asia/Shanghai for 00:00 UTC, got %02d:00", shTime.Hour())
	}
}

func TestAlertFormattersUseShanghaiTime(t *testing.T) {
	startsUTC := time.Date(2026, 6, 1, 2, 30, 45, 0, time.UTC) // 10:30:45 CST
	endsUTC := time.Date(2026, 6, 1, 3, 45, 10, 0, time.UTC)   // 11:45:10 CST

	firingData := template.Data{
		Status: "firing",
		Alerts: []template.Alert{
			{
				Status: "firing",
				Labels: template.KV{
					"alertname": "HighCPU",
					"severity":  "critical",
					"instance":  "10.0.0.1:9100",
				},
				Annotations: template.KV{
					"summary":     "CPU > 90%",
					"description": "CPU sustained high usage",
				},
				StartsAt: startsUTC,
			},
		},
	}

	wechatMsg := AlertFormatWechat(firingData)
	dingMsg := AlertFormatDingtalk(firingData)
	feishuMsg := AlertFormatFeishu(firingData)

	for name, msg := range map[string]string{
		"wechat":   wechatMsg,
		"dingtalk": dingMsg,
		"feishu":   feishuMsg,
	} {
		if !strings.Contains(msg, "2026-06-01 10:30:45") {
			t.Errorf("%s firing formatter did not convert to Asia/Shanghai time, got:\n%s", name, msg)
		}
		if !strings.Contains(msg, "HighCPU") {
			t.Errorf("%s firing formatter missing alertname", name)
		}
		if !strings.Contains(msg, "P1") {
			t.Errorf("%s firing formatter missing mapped severity P1", name)
		}
	}

	resolvedData := template.Data{
		Status: "resolved",
		Alerts: []template.Alert{
			{
				Status: "resolved",
				Labels: template.KV{
					"alertname": "HighCPU",
					"severity":  "critical",
				},
				StartsAt: startsUTC,
				EndsAt:   endsUTC,
			},
		},
	}

	for name, msg := range map[string]string{
		"wechat":   AlertFormatWechat(resolvedData),
		"dingtalk": AlertFormatDingtalk(resolvedData),
		"feishu":   AlertFormatFeishu(resolvedData),
	} {
		if !strings.Contains(msg, "2026-06-01 11:45:10") {
			t.Errorf("%s resolved formatter did not convert EndsAt to Asia/Shanghai time, got:\n%s", name, msg)
		}
	}
}

func TestSplitWeChatAlerts(t *testing.T) {
	alerts := make([]template.Alert, 0, 25)
	for i := 0; i < 25; i++ {
		alerts = append(alerts, template.Alert{
			Status: "firing",
			Labels: template.KV{
				"alertname": "BatchTestAlert",
				"severity":  "warning",
				"instance":  "node-01",
			},
			Annotations: template.KV{
				"summary":     strings.Repeat("S", 120),
				"description": strings.Repeat("D", 180),
			},
			StartsAt: time.Now(),
		})
	}

	batches := SplitWeChatAlerts(template.Data{
		Status: "firing",
		Alerts: alerts,
	})
	if len(batches) < 2 {
		t.Fatalf("expected multiple WeChat batches, got %d", len(batches))
	}
	total := 0
	for i, b := range batches {
		rendered := AlertFormatWechat(b)
		if len(rendered) > 4000 {
			t.Errorf("batch %d exceeded 4000 bytes: %d", i, len(rendered))
		}
		total += len(b.Alerts)
	}
	if total != 25 {
		t.Fatalf("expected 25 total alerts across batches, got %d", total)
	}
}

func TestFilterValidAlertsAndMappings(t *testing.T) {
	alerts := []template.Alert{
		{Labels: template.KV{"alertname": "A", "severity": "none"}},
		{Labels: template.KV{"alertname": "B", "severity": "warning"}},
	}
	valid := FilterValidAlerts(alerts)
	if len(valid) != 1 || valid[0].Labels["alertname"] != "B" {
		t.Fatalf("unexpected FilterValidAlerts result: %+v", valid)
	}

	if MapSeverity("emergency") != "P0" || MapSeverity("critical") != "P1" || MapSeverity("warning") != "P2" || MapSeverity("info") != "P3" {
		t.Fatal("unexpected MapSeverity mapping")
	}
}

func TestValidateWebhookResponse(t *testing.T) {
	cases := []struct {
		name    string
		client  string
		status  int
		body    string
		wantErr bool
	}{
		{"wechat ok", "wechat", http.StatusOK, `{"errcode":0,"errmsg":"ok"}`, false},
		{"wechat errcode", "wechat", http.StatusOK, `{"errcode":93000,"errmsg":"invalid key"}`, true},
		{"dingtalk ok", "dingtalk", http.StatusOK, `{"errcode":0,"errmsg":"ok"}`, false},
		{"dingtalk errcode", "dingtalk", http.StatusOK, `{"errcode":310000,"errmsg":"keywords not in content"}`, true},
		{"feishu v2 ok", "feishu", http.StatusOK, `{"code":0,"msg":"success"}`, false},
		{"feishu v2 error", "feishu", http.StatusOK, `{"code":19021,"msg":"sign match fail"}`, true},
		{"feishu v1 error", "feishu", http.StatusOK, `{"StatusCode":9499,"StatusMessage":"bad request"}`, true},
		{"http 500", "wechat", http.StatusInternalServerError, `internal error`, true},
		{"plain 200", "wechat", http.StatusOK, `ok`, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateWebhookResponse(tc.client, tc.status, []byte(tc.body))
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateWebhookResponse() err=%v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}
