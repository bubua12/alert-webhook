package store

import (
	"alert-webhook/config"
	"alert-webhook/utils"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAlertStoreRingBufferPersistenceAndClear(t *testing.T) {
	dir := t.TempDir()
	store1 := NewAlertStore(dir, 5)

	for i := 1; i <= 8; i++ {
		store1.Add(AlertRecord{
			Source:        "alertmanager",
			Status:        "firing",
			DeliveryState: "sent",
			AlertName:     fmt.Sprintf("Alert-%d", i),
			Severity:      "warning",
		})
	}

	recent := store1.Recent(10)
	if len(recent) != 5 {
		t.Fatalf("expected ring buffer capacity 5, got %d", len(recent))
	}
	if recent[0].AlertName != "Alert-8" || recent[4].AlertName != "Alert-4" {
		t.Fatalf("unexpected ring order: first=%s last=%s", recent[0].AlertName, recent[4].AlertName)
	}

	// Append a corrupted line to alerts.jsonl to verify reload resilience
	jsonlPath := filepath.Join(dir, "alerts.jsonl")
	f, err := os.OpenFile(jsonlPath, os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("{corrupted-json\n")
	_ = f.Close()

	// Re-open store from disk
	store2 := NewAlertStore(dir, 5)
	reloaded := store2.Recent(10)
	if len(reloaded) != 5 || reloaded[0].AlertName != "Alert-8" {
		t.Fatalf("expected 5 reloaded alerts with Alert-8 first, got %d (%+v)", len(reloaded), reloaded)
	}

	if err := store2.Clear(); err != nil {
		t.Fatalf("Clear failed: %v", err)
	}
	if len(store2.Recent(10)) != 0 {
		t.Fatal("expected empty store after Clear")
	}

	// Re-open after Clear should remain empty
	store3 := NewAlertStore(dir, 5)
	if len(store3.Recent(10)) != 0 {
		t.Fatal("expected persisted file empty after Clear")
	}
}

func TestAlertStoreUnwritableDirNonFatal(t *testing.T) {
	dir := t.TempDir()
	blockingFile := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(blockingFile, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	st := NewAlertStore(blockingFile, 10)
	rec := st.Add(AlertRecord{
		Source:        "test",
		Status:        "firing",
		DeliveryState: "sent",
		AlertName:     "InMemoryOnly",
		Severity:      "critical",
	})
	if rec.ID == "" || len(st.Recent(5)) != 1 {
		t.Fatal("expected store to operate in memory when persistence dir is unwritable")
	}
}

func TestAlertStoreQueryFiltersAndPagination(t *testing.T) {
	st := NewAlertStore("", 50)
	st.Add(AlertRecord{
		Source:        "alertmanager",
		Status:        "firing",
		DeliveryState: "sent",
		AlertName:     "HighCPU",
		Severity:      "critical",
		Instance:      "web-01",
		Summary:       "CPU 95%",
		Labels:        map[string]string{"team": "core"},
	})
	st.Add(AlertRecord{
		Source:        "traffic",
		Status:        "firing",
		DeliveryState: "partial",
		AlertName:     "LargeResponse",
		Severity:      "warning",
		Instance:      "api.example.com",
	})
	st.Add(AlertRecord{
		Source:        "kafka",
		Status:        "resolved",
		DeliveryState: "filtered",
		AlertName:     "ConsumerLag",
		Severity:      "info",
		Instance:      "orders-group",
	})

	res := st.Query(AlertQuery{Source: "traffic"})
	if res.Total != 1 || res.Items[0].AlertName != "LargeResponse" {
		t.Fatalf("source filter failed: %+v", res)
	}

	res = st.Query(AlertQuery{Severity: "P1"})
	if res.Total != 1 || res.Items[0].AlertName != "HighCPU" {
		t.Fatalf("severity code filter P1 failed: %+v", res)
	}

	res = st.Query(AlertQuery{Q: "core"})
	if res.Total != 1 || res.Items[0].AlertName != "HighCPU" {
		t.Fatalf("label keyword search failed: %+v", res)
	}

	res = st.Query(AlertQuery{Page: 2, PageSize: 2})
	if res.Total != 3 || len(res.Items) != 1 || res.Items[0].AlertName != "HighCPU" {
		t.Fatalf("pagination failed: %+v", res)
	}
}

func TestAlertStoreStatsAndTimeline(t *testing.T) {
	loc := utils.GetShanghaiLocation()
	now := time.Date(2026, 8, 10, 14, 30, 0, 0, loc)

	cfg := config.DefaultAppConfig()
	cfg.Clients = []string{"wechat", "dingtalk"}
	cfg.Notifiers["wechat"] = config.NotifierConfig{WebhookURL: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=real"}
	cfg.Notifiers["dingtalk"] = config.NotifierConfig{WebhookURL: ""}

	st := NewAlertStore("", 50)
	st.Add(AlertRecord{
		Timestamp:     now.Format(time.RFC3339),
		Source:        "alertmanager",
		Status:        "firing",
		DeliveryState: "sent",
		AlertName:     "A1",
		Severity:      "critical",
		Deliveries: []DeliveryResult{
			{Client: "wechat", Success: true, StatusCode: 200, DurationMs: 20},
		},
	})
	st.Add(AlertRecord{
		Timestamp:     now.Add(-2 * time.Hour).Format(time.RFC3339),
		Source:        "kafka",
		Status:        "resolved",
		DeliveryState: "partial",
		AlertName:     "A2",
		Severity:      "warning",
		Deliveries: []DeliveryResult{
			{Client: "wechat", Success: true, StatusCode: 200, DurationMs: 40},
			{Client: "dingtalk", Success: false, StatusCode: 500, DurationMs: 60},
		},
	})
	st.Add(AlertRecord{
		Timestamp:     now.Add(-2 * time.Hour).Format(time.RFC3339),
		Source:        "test",
		Status:        "firing",
		DeliveryState: "filtered",
		AlertName:     "A3",
		Severity:      "info",
	})

	stats := st.StatsAt(cfg, now)
	if stats.Total != 3 || stats.Firing != 2 || stats.Resolved != 1 {
		t.Fatalf("unexpected totals: %+v", stats)
	}
	if stats.Sent != 1 || stats.Partial != 1 || stats.Filtered != 1 {
		t.Fatalf("unexpected delivery state counts: %+v", stats)
	}
	if stats.SuccessRate != 66.7 {
		t.Fatalf("expected success_rate=66.7, got %v", stats.SuccessRate)
	}
	if stats.BySeverity.Critical != 1 || stats.BySeverity.Warning != 1 || stats.BySeverity.Info != 1 {
		t.Fatalf("unexpected severity counts: %+v", stats.BySeverity)
	}
	if stats.BySource.Alertmanager != 1 || stats.BySource.Kafka != 1 || stats.BySource.Test != 1 {
		t.Fatalf("unexpected source counts: %+v", stats.BySource)
	}
	wc := stats.ByChannel["wechat"]
	if !wc.Enabled || !wc.Configured || wc.Sent != 2 || wc.Failed != 0 || wc.AvgLatencyMs != 30 {
		t.Fatalf("unexpected wechat channel stats: %+v", wc)
	}
	dt := stats.ByChannel["dingtalk"]
	if !dt.Enabled || dt.Configured || dt.Sent != 0 || dt.Failed != 1 {
		t.Fatalf("unexpected dingtalk channel stats: %+v", dt)
	}
	if len(stats.Timeline) != 24 {
		t.Fatalf("expected 24 timeline buckets, got %d", len(stats.Timeline))
	}
	// Bucket 23 is current hour (14:00), bucket 21 is 2 hours ago (12:00)
	if stats.Timeline[23].Hour != "14:00" || stats.Timeline[23].Firing != 1 {
		t.Fatalf("unexpected current hour bucket: %+v", stats.Timeline[23])
	}
	if stats.Timeline[21].Hour != "12:00" || stats.Timeline[21].Resolved != 1 || stats.Timeline[21].Filtered != 1 {
		t.Fatalf("unexpected -2h bucket: %+v", stats.Timeline[21])
	}
}

func TestLogBufferWriteAndQuery(t *testing.T) {
	buf := NewLogBuffer(5)
	// Test partial chunked write + ANSI stripping
	_, _ = buf.Write([]byte("2026/11/24 10:00:00 \x1b[32m[INFO]\x1b[0m startup "))
	_, _ = buf.Write([]byte("complete\n"))
	_, _ = buf.Write([]byte("2026/11/24 10:00:01 [Warning] no clients configured\n"))
	_, _ = buf.Write([]byte("2026/11/24 10:00:02 [Error] webhook 发送失败\n"))

	items, total := buf.Query("", "", 100)
	if total != 3 || len(items) != 3 {
		t.Fatalf("expected 3 log entries, got total=%d len=%d", total, len(items))
	}
	if items[0].Level != "INFO" || items[1].Level != "WARN" || items[2].Level != "ERROR" {
		t.Fatalf("unexpected levels: %+v", items)
	}
	if items[0].Message != "2026/11/24 10:00:00 [INFO] startup complete" {
		t.Fatalf("unexpected cleaned message: %q", items[0].Message)
	}

	errItems, errTotal := buf.Query("ERROR", "发送失败", 10)
	if errTotal != 1 || len(errItems) != 1 {
		t.Fatalf("unexpected filtered ERROR logs: total=%d items=%+v", errTotal, errItems)
	}

	// Capacity eviction
	for i := 0; i < 6; i++ {
		_, _ = buf.Write([]byte(fmt.Sprintf("line-%d\n", i)))
	}
	all, allTotal := buf.Query("", "", 100)
	if allTotal != 5 || len(all) != 5 || all[4].Message != "line-5" {
		t.Fatalf("expected capacity 5 ending with line-5, got total=%d %+v", allTotal, all)
	}
}

func TestStoreAndLogBufferConcurrency(t *testing.T) {
	st := NewAlertStore(t.TempDir(), 50)
	lb := NewLogBuffer(100)
	cfg := config.DefaultAppConfig()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				st.Add(AlertRecord{
					Source:        "alertmanager",
					Status:        "firing",
					DeliveryState: "sent",
					AlertName:     fmt.Sprintf("Alert-%d-%d", id, j),
					Severity:      "warning",
				})
				_ = st.Query(AlertQuery{Page: 1, PageSize: 10})
				_ = st.Stats(cfg)
				_, _ = lb.Write([]byte(fmt.Sprintf("worker %d iter %d\n", id, j)))
				_, _ = lb.Query("", "", 50)
			}
		}(i)
	}
	wg.Wait()
}

func boolPtr(v bool) *bool {
	return &v
}

func TestAlertStoreMultiWebhookStatsPersistenceAndTargetSearch(t *testing.T) {
	dir := t.TempDir()
	jsonlPath := filepath.Join(dir, "alerts.jsonl")

	// Seed a legacy alert line without "target" field to verify backward compatibility
	legacyLine := `{"id":"legacy-1","timestamp":"2026-08-10T13:00:00+08:00","source":"alertmanager","status":"firing","delivery_state":"sent","alert_name":"LegacyAlert","severity":"warning","deliveries":[{"client":"wechat","success":true,"status_code":200,"duration_ms":15}]}` + "\n"
	if err := os.WriteFile(jsonlPath, []byte(legacyLine), 0644); err != nil {
		t.Fatal(err)
	}

	st1 := NewAlertStore(dir, 50)
	loc := utils.GetShanghaiLocation()
	now := time.Date(2026, 8, 10, 14, 30, 0, 0, loc)

	// Add multi-webhook alert record with Target names on the same Client
	st1.Add(AlertRecord{
		Timestamp:     now.Format(time.RFC3339),
		Source:        "alertmanager",
		Status:        "firing",
		DeliveryState: "partial",
		AlertName:     "MultiTargetAlert",
		Severity:      "critical",
		Deliveries: []DeliveryResult{
			{Client: "wechat", Target: "运维群", Success: true, StatusCode: 200, DurationMs: 25},
			{Client: "wechat", Target: "值班群", Success: false, StatusCode: 500, DurationMs: 50, Error: "upstream err"},
			{Client: "feishu", Target: "#1", Success: true, StatusCode: 200, DurationMs: 30},
		},
	})

	// Verify JSONL persistence and reload of Target field + legacy record compatibility
	rawJSONL, err := os.ReadFile(jsonlPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rawJSONL), `"target":"运维群"`) {
		t.Fatalf("expected target persisted in JSONL, got:\n%s", string(rawJSONL))
	}

	st2 := NewAlertStore(dir, 50)
	recent := st2.Recent(10)
	if len(recent) != 2 {
		t.Fatalf("expected 2 records after reload, got %d", len(recent))
	}
	if recent[0].Deliveries[0].Target != "运维群" || recent[0].Deliveries[1].Target != "值班群" {
		t.Fatalf("expected target preserved on reload, got %+v", recent[0].Deliveries)
	}
	if recent[1].ID != "legacy-1" || recent[1].Deliveries[0].Target != "" {
		t.Fatalf("expected legacy record intact without target, got %+v", recent[1])
	}

	// Keyword search on DeliveryResult.Target
	res := st2.Query(AlertQuery{Q: "值班群"})
	if res.Total != 1 || res.Items[0].AlertName != "MultiTargetAlert" {
		t.Fatalf("expected query on target name '值班群' to match MultiTargetAlert, got %+v", res)
	}

	// StatsAt with multi-webhook ConfiguredCount / TotalCount and multi-delivery accumulation
	cfg := config.DefaultAppConfig()
	cfg.Clients = []string{"wechat", "feishu"}
	cfg.Notifiers["wechat"] = config.NotifierConfig{
		Webhooks: []config.WebhookTarget{
			{Name: "运维群", URL: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=k1"},
			{Name: "值班群", URL: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=k2"},
			{Name: "停用群", URL: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=k3", Enabled: boolPtr(false)},
			{Name: "占位群", URL: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=xxxxxx"},
		},
	}
	cfg.Notifiers["feishu"] = config.NotifierConfig{
		Webhooks: []config.WebhookTarget{
			{URL: "https://open.feishu.cn/open-apis/bot/v2/hook/real1"},
		},
	}
	cfg.ApplyDefaults()

	stats := st2.StatsAt(cfg, now)
	wc := stats.ByChannel["wechat"]
	if !wc.Enabled || !wc.Configured || wc.ConfiguredCount != 2 || wc.TotalCount != 4 {
		t.Fatalf("unexpected wechat multi-webhook counts: %+v", wc)
	}
	// Legacy wechat delivery (15ms, success) + 运维群 (25ms, success) + 值班群 (50ms, fail) => sent=2, failed=1, avg=(15+25+50)/3 = 30.0
	if wc.Sent != 2 || wc.Failed != 1 || wc.AvgLatencyMs != 30 {
		t.Fatalf("unexpected wechat delivery accumulation: %+v", wc)
	}

	fs := stats.ByChannel["feishu"]
	if !fs.Enabled || !fs.Configured || fs.ConfiguredCount != 1 || fs.TotalCount != 1 || fs.Sent != 1 {
		t.Fatalf("unexpected feishu stats: %+v", fs)
	}

	dt := stats.ByChannel["dingtalk"]
	if dt.Enabled || dt.Configured || dt.ConfiguredCount != 0 || dt.TotalCount != 0 {
		t.Fatalf("unexpected dingtalk stats: %+v", dt)
	}
}
