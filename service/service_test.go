package service

import (
	"alert-webhook/config"
	"alert-webhook/store"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/prometheus/alertmanager/template"
)

func setupTestEnv(t *testing.T, yamlContent string) (*config.ConfigManager, *store.AlertStore, *store.LogBuffer, *ServiceManager, *ServerManager) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if yamlContent != "" {
		if err := os.WriteFile(cfgPath, []byte(yamlContent), 0644); err != nil {
			t.Fatal(err)
		}
	}
	cfgMgr, err := config.NewConfigManager(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	alertStore := store.NewAlertStore(filepath.Join(dir, "data"), 100)
	logBuffer := store.NewLogBuffer(100)
	svcMgr := NewServiceManager(cfgMgr, alertStore)
	cfgMgr.OnReload(func(c *config.AppConfig) {
		svcMgr.Reload(c)
	})
	srvMgr := NewServerManager(cfgMgr, alertStore, logBuffer, svcMgr)
	t.Cleanup(func() {
		svcMgr.Shutdown()
		RegisterUIFS(nil)
	})
	return cfgMgr, alertStore, logBuffer, svcMgr, srvMgr
}

func TestSendToClientsValidatesJSONErrorCodesAndParallelRace(t *testing.T) {
	wechatSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	defer wechatSrv.Close()

	dingErrSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"errcode":310000,"errmsg":"sign not match"}`))
	}))
	defer dingErrSrv.Close()

	feishuErrSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"code":19021,"msg":"forbidden"}`))
	}))
	defer feishuErrSrv.Close()

	data := template.Data{
		Status: "firing",
		Alerts: []template.Alert{
			{
				Status:      "firing",
				Labels:      template.KV{"alertname": "HighCPU", "severity": "critical", "instance": "node-1"},
				Annotations: template.KV{"summary": "CPU high", "description": "95%"},
				StartsAt:    time.Now(),
			},
		},
	}

	results := SendToClients(data, []string{"wechat", "dingtalk", "feishu"}, map[string]string{
		"wechat":   wechatSrv.URL,
		"dingtalk": dingErrSrv.URL,
		"feishu":   feishuErrSrv.URL,
	})
	if len(results) != 3 {
		t.Fatalf("expected 3 delivery results, got %d", len(results))
	}

	byClient := map[string]store.DeliveryResult{}
	for _, r := range results {
		byClient[r.Client] = r
	}
	if !byClient["wechat"].Success {
		t.Fatalf("expected wechat success, got %+v", byClient["wechat"])
	}
	if byClient["dingtalk"].Success || !strings.Contains(byClient["dingtalk"].Error, "310000") {
		t.Fatalf("expected dingtalk errcode failure on HTTP 200, got %+v", byClient["dingtalk"])
	}
	if byClient["feishu"].Success || !strings.Contains(byClient["feishu"].Error, "19021") {
		t.Fatalf("expected feishu code failure on HTTP 200, got %+v", byClient["feishu"])
	}
	if state := ComputeDeliveryState(results); state != "partial" {
		t.Fatalf("expected partial state, got %s", state)
	}
}

func TestWebhookHTTPTimeout(t *testing.T) {
	slowSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer slowSrv.Close()

	shortClient := &http.Client{Timeout: 40 * time.Millisecond}
	_, _, err := sendAlertWithClient(shortClient, "wechat", slowSrv.URL, map[string]string{"a": "b"})
	if err == nil {
		t.Fatal("expected HTTP timeout error")
	}
}

func TestAlertmanagerWebhookRecordsAllAndConcurrency(t *testing.T) {
	var okCalls atomic.Int32
	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		okCalls.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	defer okSrv.Close()

	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"errcode":40001,"errmsg":"invalid token"}`))
	}))
	defer failSrv.Close()

	initialYAML := `server:
  port: "127.0.0.1:18082"
client:
  - wechat
  - dingtalk
notifiers:
  wechat:
    webhook_url: "` + okSrv.URL + `"
  dingtalk:
    webhook_url: "` + failSrv.URL + `"
filter:
  severity:
    exclude:
      - "info"
      - "none"
`
	_, alertStore, _, _, srvMgr := setupTestEnv(t, initialYAML)
	router := srvMgr.BuildRouter()

	payload := `{
		"status": "firing",
		"alerts": [
			{"status":"firing","labels":{"alertname":"DiskFull","severity":"critical","instance":"db-1"},"annotations":{"summary":"disk 95%"}},
			{"status":"firing","labels":{"alertname":"InfoNoise","severity":"info","instance":"db-1"}},
			{"status":"firing","labels":{"alertname":"NoneNoise","severity":"none","instance":"db-1"}}
		]
	}`

	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/webhook-alert", strings.NewReader(payload))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusInternalServerError {
				t.Errorf("expected 500 on partial client failure, got %d (%s)", rec.Code, rec.Body.String())
			}
		}()
	}
	wg.Wait()

	if okCalls.Load() != 6 {
		t.Fatalf("expected 6 wechat calls, got %d", okCalls.Load())
	}

	// Each of 6 requests had 3 alerts (1 partial + 2 filtered) = 18 records
	all := alertStore.Query(store.AlertQuery{Page: 1, PageSize: 50})
	if all.Total != 18 {
		t.Fatalf("expected 18 recorded alerts (including filtered), got %d", all.Total)
	}
	filtered := alertStore.Query(store.AlertQuery{DeliveryState: "filtered", Page: 1, PageSize: 50})
	if filtered.Total != 12 {
		t.Fatalf("expected 12 filtered alerts, got %d", filtered.Total)
	}
}

func TestClickHouseIdentifierValidation(t *testing.T) {
	if _, err := resolveClickHouseDB("nginxlogs"); err != nil {
		t.Fatalf("expected valid db name, got %v", err)
	}
	if db, err := resolveClickHouseDB(""); err != nil || db != "nginxlogs" {
		t.Fatalf("expected empty db to default to nginxlogs, got %q err=%v", db, err)
	}
	if _, err := resolveClickHouseDB("nginx; DROP TABLE x"); err == nil {
		t.Fatal("expected error for SQL injection in ClickHouse database name")
	}
}

func TestKafkaMonitorServiceCheckNowAndStopIdempotency(t *testing.T) {
	webhookSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"msg":"success"}`))
	}))
	defer webhookSrv.Close()

	kafkaUISrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/api/clusters/prod-cluster/consumer-groups/paged") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{
			"pageCount": 1,
			"consumerGroups": [
				{"groupId":"orders","members":2,"topics":1,"state":"STABLE","consumerLag":150000,"coordinator":{"id":1,"host":"k1","port":9092}},
				{"groupId":"ignored-grp","members":1,"topics":1,"state":"STABLE","consumerLag":500000,"coordinator":{"id":1,"host":"k1","port":9092}},
				{"groupId":"healthy","members":2,"topics":1,"state":"STABLE","consumerLag":10,"coordinator":{"id":1,"host":"k1","port":9092}}
			]
		}`))
	}))
	defer kafkaUISrv.Close()

	yamlCfg := `server:
  port: "127.0.0.1:18082"
client:
  - feishu
notifiers:
  feishu:
    webhook_url: "` + webhookSrv.URL + `"
kafka_monitor:
  enabled: true
  kafka_ui_url: "` + kafkaUISrv.URL + `"
  cluster_name: "prod-cluster"
  check_interval: 1
  consumer_lag_threshold: 100000
  monitored_groups: []
  ignored_groups:
    - "ignored-grp"
`
	cfgMgr, alertStore, _, svcMgr, _ := setupTestEnv(t, yamlCfg)
	svcMgr.InitializeAll(cfgMgr.Get())

	status, err := svcMgr.TriggerKafkaCheck()
	if err != nil {
		t.Fatalf("TriggerKafkaCheck failed: %v", err)
	}
	if !status.Healthy || status.TotalGroups != 3 || status.LaggedLast != 1 || status.TotalAlerts != 1 {
		t.Fatalf("unexpected Kafka status: %+v", status)
	}

	records := alertStore.Query(store.AlertQuery{Source: "kafka"})
	if records.Total != 1 || records.Items[0].Instance != "orders" || records.Items[0].DeliveryState != "sent" {
		t.Fatalf("unexpected Kafka alert record: %+v", records)
	}

	// Stop must be safe and idempotent
	svcMgr.kafkaMonitorService.Stop()
	svcMgr.kafkaMonitorService.Stop()
}

func TestRESTAPIEndpointsAuthStaticUIAndSimulate(t *testing.T) {
	notifierSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	defer notifierSrv.Close()

	kafkaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"pageCount":1,"consumerGroups":[{"groupId":"g1","members":1,"topics":1,"state":"STABLE","consumerLag":5}]}`))
	}))
	defer kafkaSrv.Close()

	yamlCfg := `server:
  port: "127.0.0.1:18082"
  ui_enabled: true
  auth_token: "my-secret-token"
client:
  - wechat
notifiers:
  wechat:
    webhook_url: "` + notifierSrv.URL + `"
kafka_monitor:
  enabled: false
  kafka_ui_url: "` + kafkaSrv.URL + `"
  cluster_name: "test-cluster"
filter:
  alert_name:
    exclude:
      - "*Blocked*"
  severity:
    exclude:
      - "info"
      - "none"
`
	_, alertStore, logBuffer, _, srvMgr := setupTestEnv(t, yamlCfg)
	_, _ = logBuffer.Write([]byte("2026/11/24 11:00:00 [Info] ready for traffic\n"))
	_, _ = logBuffer.Write([]byte("2026/11/24 11:00:01 [Error] sample error log\n"))

	RegisterUIFS(fstest.MapFS{
		"index.html":    &fstest.MapFile{Data: []byte("<html><body>Console UI</body></html>")},
		"css/style.css": &fstest.MapFile{Data: []byte("body { color: #111; }")},
		"js/app.js":     &fstest.MapFile{Data: []byte("console.log('ok');")},
	})

	router := srvMgr.BuildRouter()

	// 1. Public routes bypass auth
	for _, p := range []string{"/healthz", "/readyz", "/", "/css/style.css", "/js/app.js", "/alerts"} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s expected 200 without auth, got %d", p, rec.Code)
		}
		if p == "/" && rec.Header().Get("Location") != "" {
			t.Fatalf("GET / must not redirect, got Location=%s", rec.Header().Get("Location"))
		}
	}

	// 2. /api/* requires auth when auth_token is set
	unauthReq := httptest.NewRequest(http.MethodGet, "/api/overview", nil)
	unauthRec := httptest.NewRecorder()
	router.ServeHTTP(unauthRec, unauthReq)
	if unauthRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", unauthRec.Code)
	}

	doAPI := func(method, p, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, p, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer my-secret-token")
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	// 3. GET /api/overview (X-API-Token also accepted)
	ovReq := httptest.NewRequest(http.MethodGet, "/api/overview", nil)
	ovReq.Header.Set("X-API-Token", "my-secret-token")
	ovRec := httptest.NewRecorder()
	router.ServeHTTP(ovRec, ovReq)
	if ovRec.Code != http.StatusOK {
		t.Fatalf("GET /api/overview expected 200, got %d", ovRec.Code)
	}
	var ovBody map[string]interface{}
	if err := json.Unmarshal(ovRec.Body.Bytes(), &ovBody); err != nil {
		t.Fatal(err)
	}
	if ovBody["version"] != "1.2.0" || ovBody["setup_complete"] != true || ovBody["auth_required"] != true {
		t.Fatalf("unexpected overview payload: %+v", ovBody)
	}

	// 4. Simulate dry-run does NOT pollute AlertStore
	simDry := doAPI(http.MethodPost, "/api/simulate", `{"status":"firing","alert_name":"SimCPU","severity":"critical","instance":"n1","summary":"s","description":"d","send":false}`)
	if simDry.Code != http.StatusOK {
		t.Fatalf("simulate dry run failed: %d %s", simDry.Code, simDry.Body.String())
	}
	if alertStore.Query(store.AlertQuery{}).Total != 0 {
		t.Fatal("dry_run simulate must not add record to AlertStore")
	}

	// 5. Simulate send=true delivers and records source="test"
	simSend := doAPI(http.MethodPost, "/api/simulate", `{"status":"firing","alert_name":"SimCPU","severity":"critical","instance":"n1","summary":"s","description":"d","send":true}`)
	if simSend.Code != http.StatusOK {
		t.Fatalf("simulate send failed: %d %s", simSend.Code, simSend.Body.String())
	}
	if alertStore.Query(store.AlertQuery{Source: "test"}).Total != 1 {
		t.Fatal("expected 1 test record in AlertStore after simulate send=true")
	}

	// 6. Simulate send=true when blocked by filter records delivery_state="filtered"
	simBlocked := doAPI(http.MethodPost, "/api/simulate", `{"status":"firing","alert_name":"BlockedAlert","severity":"critical","send":true}`)
	if simBlocked.Code != http.StatusOK {
		t.Fatalf("simulate blocked failed: %d", simBlocked.Code)
	}
	if alertStore.Query(store.AlertQuery{DeliveryState: "filtered"}).Total != 1 {
		t.Fatal("expected blocked simulate to record filtered alert")
	}

	// 7. GET & DELETE /api/alerts
	listRec := doAPI(http.MethodGet, "/api/alerts?page=1&page_size=10", "")
	if listRec.Code != http.StatusOK || !strings.Contains(listRec.Body.String(), "SimCPU") {
		t.Fatalf("GET /api/alerts unexpected: %d %s", listRec.Code, listRec.Body.String())
	}
	delRec := doAPI(http.MethodDelete, "/api/alerts", "")
	if delRec.Code != http.StatusOK || alertStore.Query(store.AlertQuery{}).Total != 0 {
		t.Fatalf("DELETE /api/alerts failed: %d", delRec.Code)
	}

	// 8. POST /api/filter/test
	fltRec := doAPI(http.MethodPost, "/api/filter/test", `{"alert_name":"CustomAlert","severity":"warning","filter":{"alert_name":{"include":["Custom*"],"exclude":[]},"severity":{"include":[],"exclude":["info"]}}}`)
	if fltRec.Code != http.StatusOK || !strings.Contains(fltRec.Body.String(), `"allowed":true`) {
		t.Fatalf("POST /api/filter/test unexpected: %d %s", fltRec.Code, fltRec.Body.String())
	}

	// 9. POST /api/test/notifier
	tnRec := doAPI(http.MethodPost, "/api/test/notifier", `{"client":"wechat","webhook_url":"`+notifierSrv.URL+`"}`)
	if tnRec.Code != http.StatusOK || !strings.Contains(tnRec.Body.String(), `"ok":true`) {
		t.Fatalf("POST /api/test/notifier failed: %d %s", tnRec.Code, tnRec.Body.String())
	}

	// 10. POST /api/test/kafka & clickhouse
	tkRec := doAPI(http.MethodPost, "/api/test/kafka", `{}`)
	if tkRec.Code != http.StatusOK || !strings.Contains(tkRec.Body.String(), `"group_count":1`) {
		t.Fatalf("POST /api/test/kafka failed: %d %s", tkRec.Code, tkRec.Body.String())
	}
	tchRec := doAPI(http.MethodPost, "/api/test/clickhouse", `{"host":"127.0.0.1","port":9000,"database":"bad;db"}`)
	if tchRec.Code != http.StatusOK || !strings.Contains(tchRec.Body.String(), `"ok":false`) {
		t.Fatalf("POST /api/test/clickhouse unexpected: %d %s", tchRec.Code, tchRec.Body.String())
	}

	// 11. GET /api/config, PUT /api/config (400 + 200), PUT /api/config/yaml, POST /api/config/reload
	getCfgRec := doAPI(http.MethodGet, "/api/config", "")
	if getCfgRec.Code != http.StatusOK {
		t.Fatalf("GET /api/config failed: %d", getCfgRec.Code)
	}
	badPutRec := doAPI(http.MethodPut, "/api/config", `{"server":{"port":"invalid"},"client":[],"notifiers":{}}`)
	if badPutRec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid config, got %d", badPutRec.Code)
	}
	var validCfgResp struct {
		Config config.AppConfig `json:"config"`
	}
	if err := json.Unmarshal(getCfgRec.Body.Bytes(), &validCfgResp); err != nil {
		t.Fatal(err)
	}
	validCfgResp.Config.Filter.AlertName.Exclude = []string{"Noise*"}
	updatedBytes, _ := json.Marshal(validCfgResp.Config)
	putRec := doAPI(http.MethodPut, "/api/config", string(updatedBytes))
	if putRec.Code != http.StatusOK {
		t.Fatalf("PUT /api/config failed: %d %s", putRec.Code, putRec.Body.String())
	}

	// Test PUT /api/config/yaml disabling UI and verifying "/" returns 404
	rawYAMLBody, _ := json.Marshal(map[string]string{
		"yaml": "server:\n  port: \"127.0.0.1:18082\"\n  ui_enabled: false\n  auth_token: \"my-secret-token\"\nclient: []\nnotifiers: {}\n",
	})
	putYAMLRec := doAPI(http.MethodPut, "/api/config/yaml", string(rawYAMLBody))
	if putYAMLRec.Code != http.StatusOK {
		t.Fatalf("PUT /api/config/yaml failed: %d %s", putYAMLRec.Code, putYAMLRec.Body.String())
	}
	uiDisabledReq := httptest.NewRequest(http.MethodGet, "/", nil)
	uiDisabledRec := httptest.NewRecorder()
	router.ServeHTTP(uiDisabledRec, uiDisabledReq)
	if uiDisabledRec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 on / when ui_enabled=false, got %d", uiDisabledRec.Code)
	}

	reloadRec := doAPI(http.MethodPost, "/api/config/reload", "")
	if reloadRec.Code != http.StatusOK {
		t.Fatalf("POST /api/config/reload failed: %d", reloadRec.Code)
	}

	// 12. GET /api/logs
	logsRec := doAPI(http.MethodGet, "/api/logs?level=ERROR", "")
	if logsRec.Code != http.StatusOK || !strings.Contains(logsRec.Body.String(), "sample error log") {
		t.Fatalf("GET /api/logs unexpected: %d %s", logsRec.Code, logsRec.Body.String())
	}

	// 13. Non-existent /api/* returns 404 JSON
	notFoundAPI := doAPI(http.MethodGet, "/api/does-not-exist", "")
	if notFoundAPI.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown API path, got %d", notFoundAPI.Code)
	}

	// 14. Services check endpoints when disabled return ok: false
	tcRec := doAPI(http.MethodPost, "/api/services/traffic/check", "")
	if tcRec.Code != http.StatusOK || !strings.Contains(tcRec.Body.String(), `"ok":false`) {
		t.Fatalf("unexpected traffic check when disabled: %d %s", tcRec.Code, tcRec.Body.String())
	}

	// 15. Graceful ServerManager Start + Shutdown
	errCh := make(chan error, 1)
	go func() {
		errCh <- srvMgr.StartWebhookServer("127.0.0.1:0")
	}()
	time.Sleep(50 * time.Millisecond)
	if err := srvMgr.Shutdown(); err != nil {
		t.Fatalf("ServerManager Shutdown failed: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("StartWebhookServer returned unexpected error: %v", err)
	}
}
