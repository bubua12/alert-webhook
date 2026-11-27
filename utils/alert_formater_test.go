package utils

import (
	"errors"
	"net/http"
	"strconv"
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

func TestValidateWebhookResponseRateLimit(t *testing.T) {
	cases := []struct {
		name        string
		client      string
		status      int
		body        string
		rateLimited bool
	}{
		{"wechat 45009", "wechat", http.StatusOK, `{"errcode":45009,"errmsg":"api freq out of limit"}`, true},
		{"wechat 40001", "wechat", http.StatusOK, `{"errcode":40001,"errmsg":"invalid credential"}`, false},
		{"dingtalk 130101", "dingtalk", http.StatusOK, `{"errcode":130101,"errmsg":"too many messages"}`, true},
		{"dingtalk 310000", "dingtalk", http.StatusOK, `{"errcode":310000,"errmsg":"sign error"}`, false},
		{"feishu 11232", "feishu", http.StatusOK, `{"code":11232,"msg":"rate limited"}`, true},
		{"feishu 99991400", "feishu", http.StatusOK, `{"code":99991400,"msg":"frequency limit"}`, true},
		{"feishu statuscode 11232", "feishu", http.StatusOK, `{"StatusCode":11232,"StatusMessage":"rate limited"}`, true},
		{"feishu 19021", "feishu", http.StatusOK, `{"code":19021,"msg":"forbidden"}`, false},
		{"http 429", "wechat", http.StatusTooManyRequests, `too many requests`, true},
		{"http 500", "wechat", http.StatusInternalServerError, `server error`, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateWebhookResponse(tc.client, tc.status, []byte(tc.body))
			if err == nil {
				t.Fatal("expected error")
			}
			if IsRateLimitError(err) != tc.rateLimited {
				t.Fatalf("IsRateLimitError(%v) = %v, want %v", err, IsRateLimitError(err), tc.rateLimited)
			}
		})
	}

	if IsRateLimitError(nil) {
		t.Fatal("IsRateLimitError(nil) must be false")
	}
	if IsRateLimitError(errors.New("plain error")) {
		t.Fatal("IsRateLimitError(plain) must be false")
	}
}

func makeStormTestData(count int) template.Data {
	alerts := make([]template.Alert, 0, count)
	now := time.Now()
	for i := 0; i < count; i++ {
		alerts = append(alerts, template.Alert{
			Status: "firing",
			Labels: template.KV{
				"alertname": "KubePodNotReady",
				"severity":  "warning",
				"instance":  "pod-" + strconv.Itoa(i),
			},
			Annotations: template.KV{
				"summary":     "Pod has been in a non-ready state for more than 15 minutes.",
				"description": "Pod syncplant-backend/pod-" + strconv.Itoa(i) + " " + strings.Repeat("d", 140),
			},
			StartsAt: now,
		})
	}
	return template.Data{
		Status: "firing",
		Alerts: alerts,
	}
}

func TestRenderWeChatMessagesStormCollapse(t *testing.T) {
	data := makeStormTestData(500)
	msgs, omitted := RenderWeChatMessages(data, 5)
	if len(msgs) == 0 || len(msgs) > 5 {
		t.Fatalf("expected 1..5 batches, got %d", len(msgs))
	}
	if omitted <= 0 {
		t.Fatalf("expected omitted > 0 for 500 alerts, got %d", omitted)
	}
	for i, msg := range msgs {
		if len(msg) > wechatMaxMessageBytes {
			t.Fatalf("batch %d exceeded %d bytes: %d", i, wechatMaxMessageBytes, len(msg))
		}
	}
	last := msgs[len(msgs)-1]
	if !strings.Contains(last, "告警风暴折叠") || !strings.Contains(last, "500") || !strings.Contains(last, strconv.Itoa(omitted)) {
		t.Fatalf("expected collapse notice in last batch, got:\n%s", last)
	}
}

func TestFormatDingtalkAndFeishuCapped(t *testing.T) {
	small := makeStormTestData(2)
	dtSmall, dtOmitted0 := FormatDingtalkCapped(small, 16000)
	if dtOmitted0 != 0 || dtSmall != AlertFormatDingtalk(small) {
		t.Fatalf("expected uncapped DingTalk output to match AlertFormatDingtalk, omitted=%d", dtOmitted0)
	}
	fsSmall, fsOmitted0 := FormatFeishuCapped(small, 20000)
	if fsOmitted0 != 0 || fsSmall != AlertFormatFeishu(small) {
		t.Fatalf("expected uncapped Feishu output to match AlertFormatFeishu, omitted=%d", fsOmitted0)
	}

	storm := makeStormTestData(200)
	dtMsg, dtOmitted := FormatDingtalkCapped(storm, 4000)
	if dtOmitted <= 0 || len(dtMsg) > 4000 {
		t.Fatalf("expected truncated DingTalk message <= 4000B with omitted > 0, len=%d omitted=%d", len(dtMsg), dtOmitted)
	}
	if !strings.Contains(dtMsg, "告警风暴折叠") || !strings.Contains(dtMsg, "200") || !strings.Contains(dtMsg, strconv.Itoa(dtOmitted)) {
		t.Fatalf("missing DingTalk collapse notice:\n%s", dtMsg)
	}

	fsMsg, fsOmitted := FormatFeishuCapped(storm, 4000)
	if fsOmitted <= 0 || len(fsMsg) > 4000 {
		t.Fatalf("expected truncated Feishu message <= 4000B with omitted > 0, len=%d omitted=%d", len(fsMsg), fsOmitted)
	}
	if !strings.Contains(fsMsg, "告警风暴折叠") || !strings.Contains(fsMsg, "200") || !strings.Contains(fsMsg, strconv.Itoa(fsOmitted)) {
		t.Fatalf("missing Feishu collapse notice:\n%s", fsMsg)
	}
}
