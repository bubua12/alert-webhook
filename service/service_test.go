package service

import (
	"alert-webhook/config"
	"alert-webhook/store"
	"bytes"
	"encoding/json"
	"fmt"
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

	// 2. /api/* requires auth when auth_token is set; /api/auth/status and /api/auth/login stay reachable
	unauthReq := httptest.NewRequest(http.MethodGet, "/api/overview", nil)
	unauthRec := httptest.NewRecorder()
	router.ServeHTTP(unauthRec, unauthReq)
	if unauthRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", unauthRec.Code)
	}

	statusUnauth := httptest.NewRequest(http.MethodGet, "/api/auth/status", nil)
	statusUnauthRec := httptest.NewRecorder()
	router.ServeHTTP(statusUnauthRec, statusUnauth)
	if statusUnauthRec.Code != http.StatusOK || !strings.Contains(statusUnauthRec.Body.String(), `"authenticated":false`) || !strings.Contains(statusUnauthRec.Body.String(), `"auth_required":true`) {
		t.Fatalf("unexpected unauth /api/auth/status response: %d %s", statusUnauthRec.Code, statusUnauthRec.Body.String())
	}

	loginBad := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"api_key":"wrong-key"}`))
	loginBad.Header.Set("Content-Type", "application/json")
	loginBadRec := httptest.NewRecorder()
	router.ServeHTTP(loginBadRec, loginBad)
	if loginBadRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for wrong api_key, got %d %s", loginBadRec.Code, loginBadRec.Body.String())
	}

	loginOK := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"api_key":"my-secret-token"}`))
	loginOK.Header.Set("Content-Type", "application/json")
	loginOKRec := httptest.NewRecorder()
	router.ServeHTTP(loginOKRec, loginOK)
	if loginOKRec.Code != http.StatusOK || !strings.Contains(loginOKRec.Body.String(), `"authenticated":true`) {
		t.Fatalf("expected 200 for valid api_key, got %d %s", loginOKRec.Code, loginOKRec.Body.String())
	}

	statusAuthed := httptest.NewRequest(http.MethodGet, "/api/auth/status", nil)
	statusAuthed.Header.Set("X-API-Key", "my-secret-token")
	statusAuthedRec := httptest.NewRecorder()
	router.ServeHTTP(statusAuthedRec, statusAuthed)
	if statusAuthedRec.Code != http.StatusOK || !strings.Contains(statusAuthedRec.Body.String(), `"authenticated":true`) {
		t.Fatalf("unexpected authed /api/auth/status response: %d %s", statusAuthedRec.Code, statusAuthedRec.Body.String())
	}

	ovKeyReq := httptest.NewRequest(http.MethodGet, "/api/overview", nil)
	ovKeyReq.Header.Set("X-API-Key", "my-secret-token")
	ovKeyRec := httptest.NewRecorder()
	router.ServeHTTP(ovKeyRec, ovKeyReq)
	if ovKeyRec.Code != http.StatusOK {
		t.Fatalf("expected X-API-Key to authenticate /api/overview, got %d", ovKeyRec.Code)
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
	if ovBody["version"] != "1.2.1" || ovBody["setup_complete"] != true || ovBody["auth_required"] != true {
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

func testBoolPtr(v bool) *bool {
	return &v
}

func TestSendToClientTargetsMultiWebhookConcurrencyAndSanitization(t *testing.T) {
	if tag := formatClientLogTag("wechat", "运维群"); tag != "wechat/运维群" {
		t.Fatalf("unexpected log tag: %q", tag)
	}
	if tag := formatClientLogTag("wechat", "#2"); tag != "wechat/#2" {
		t.Fatalf("unexpected log tag: %q", tag)
	}
	if tag := formatClientLogTag("wechat", ""); tag != "wechat" {
		t.Fatalf("unexpected log tag: %q", tag)
	}

	var wechat1Calls, wechat2Calls, ding1Calls, ding2Calls, feishu1Calls, feishu2Calls, disabledCalls atomic.Int32

	wc1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wechat1Calls.Add(1)
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	defer wc1.Close()

	wc2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wechat2Calls.Add(1)
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	defer wc2.Close()

	dt1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ding1Calls.Add(1)
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	defer dt1.Close()

	dt2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ding2Calls.Add(1)
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	defer dt2.Close()

	fs1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		feishu1Calls.Add(1)
		_, _ = w.Write([]byte(`{"code":0,"msg":"success"}`))
	}))
	defer fs1.Close()

	fs2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		feishu2Calls.Add(1)
		_, _ = w.Write([]byte(`{"code":0,"msg":"success"}`))
	}))
	defer fs2.Close()

	disabledSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		disabledCalls.Add(1)
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	defer disabledSrv.Close()

	// Build large alert payload so WeChat splits into 2 batches across both concurrent targets.
	longDesc := strings.Repeat("x", 2200)
	data := template.Data{
		Status: "firing",
		Alerts: []template.Alert{
			{
				Status:      "firing",
				Labels:      template.KV{"alertname": "MultiAlert1", "severity": "critical", "instance": "node-1"},
				Annotations: template.KV{"summary": "s1", "description": longDesc},
				StartsAt:    time.Now(),
			},
			{
				Status:      "firing",
				Labels:      template.KV{"alertname": "MultiAlert2", "severity": "warning", "instance": "node-2"},
				Annotations: template.KV{"summary": "s2", "description": longDesc},
				StartsAt:    time.Now(),
			},
		},
	}

	notifiers := map[string][]config.WebhookTarget{
		"wechat": {
			{Name: "运维群", URL: wc1.URL + "/send?key=secret-wc1"},
			{Name: "停用群", URL: disabledSrv.URL, Enabled: testBoolPtr(false)},
			{Name: "值班群", URL: wc2.URL + "/send?key=secret-wc2"},
		},
		"dingtalk": {
			{URL: dt1.URL + "/robot/send?access_token=secret-dt1"},
			{URL: dt2.URL + "/robot/send?access_token=secret-dt2"},
		},
		"feishu": {
			{Name: "SRE", URL: fs1.URL + "/open-apis/bot/v2/hook/secret-fs1"},
			{URL: fs2.URL + "/open-apis/bot/v2/hook/secret-fs2"},
		},
	}

	// Duplicate client entries must be deduplicated preserving order.
	results := SendToClientTargets(data, []string{"wechat", "dingtalk", "wechat", "feishu", "dingtalk"}, notifiers)
	if len(results) != 6 {
		t.Fatalf("expected 6 delivery results, got %d: %+v", len(results), results)
	}
	if disabledCalls.Load() != 0 {
		t.Fatalf("disabled webhook target must not be called, got %d calls", disabledCalls.Load())
	}
	if wechat1Calls.Load() != 2 || wechat2Calls.Load() != 2 {
		t.Fatalf("expected 2 wechat batches per target, got wc1=%d wc2=%d", wechat1Calls.Load(), wechat2Calls.Load())
	}
	if ding1Calls.Load() != 1 || ding2Calls.Load() != 1 || feishu1Calls.Load() != 1 || feishu2Calls.Load() != 1 {
		t.Fatalf("unexpected call counts: dt1=%d dt2=%d fs1=%d fs2=%d", ding1Calls.Load(), ding2Calls.Load(), feishu1Calls.Load(), feishu2Calls.Load())
	}

	expectedOrder := []struct {
		client string
		target string
	}{
		{"wechat", "运维群"},
		{"wechat", "值班群"},
		{"dingtalk", "#1"},
		{"dingtalk", "#2"},
		{"feishu", "SRE"},
		{"feishu", "#2"},
	}
	for i, exp := range expectedOrder {
		if results[i].Client != exp.client || results[i].Target != exp.target || !results[i].Success {
			t.Fatalf("result[%d] mismatch: expected (%s, %s, success=true), got %+v", i, exp.client, exp.target, results[i])
		}
	}
	if results[0].Batches != 2 || results[1].Batches != 2 {
		t.Fatalf("expected Batches=2 for both wechat targets, got %+v", results[:2])
	}

	// Verify URL secret redaction on dial error and placeholder/disabled errors.
	closedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	closedURL := closedSrv.URL
	closedSrv.Close()

	secretToken := "super-secret-token-999"
	errResults := SendToClientTargets(data, []string{"wechat", "dingtalk", "feishu"}, map[string][]config.WebhookTarget{
		"wechat": {
			{Name: "故障群", URL: closedURL + "/cgi-bin/webhook/send?key=" + secretToken},
			{Name: "占位群", URL: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=xxxxxx"},
		},
		"dingtalk": {
			{Name: "停用", URL: dt1.URL, Enabled: testBoolPtr(false)},
		},
		// feishu omitted -> 0 enabled targets
	})
	if len(errResults) != 4 {
		t.Fatalf("expected 4 error results, got %d: %+v", len(errResults), errResults)
	}
	if errResults[0].Success || errResults[0].Target != "故障群" {
		t.Fatalf("expected 故障群 failure, got %+v", errResults[0])
	}
	if strings.Contains(errResults[0].Error, secretToken) || strings.Contains(errResults[0].Response, secretToken) {
		t.Fatalf("webhook secret leaked in result: %+v", errResults[0])
	}
	if errResults[1].Success || errResults[1].Target != "占位群" || !strings.Contains(errResults[1].Error, "占位符") {
		t.Fatalf("expected placeholder failure on 占位群, got %+v", errResults[1])
	}
	if errResults[2].Success || !strings.Contains(errResults[2].Error, "未配置已启用的 Webhook URL") {
		t.Fatalf("expected disabled-only dingtalk failure, got %+v", errResults[2])
	}
	if errResults[3].Success || !strings.Contains(errResults[3].Error, "未配置已启用的 Webhook URL") {
		t.Fatalf("expected unconfigured feishu failure, got %+v", errResults[3])
	}
}

func TestAlertmanagerMultiWebhookAllSuccessAndPartialFailure(t *testing.T) {
	var ok1Calls, ok2Calls, disabledCalls atomic.Int32
	ok1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ok1Calls.Add(1)
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	defer ok1.Close()

	ok2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ok2Calls.Add(1)
		_, _ = w.Write([]byte(`{"code":0,"msg":"success"}`))
	}))
	defer ok2.Close()

	failTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errcode":40001,"errmsg":"bad token"}`))
	}))
	defer failTarget.Close()

	disabledTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		disabledCalls.Add(1)
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	defer disabledTarget.Close()

	// 1. All enabled targets succeed -> 200 and delivery_state="sent"
	allOKYAML := `server:
  port: "127.0.0.1:18082"
client:
  - wechat
  - feishu
notifiers:
  wechat:
    webhooks:
      - name: "告警主群"
        url: "` + ok1.URL + `"
      - name: "静默群"
        url: "` + disabledTarget.URL + `"
        enabled: false
  feishu:
    webhooks:
      - name: "值班飞书"
        url: "` + ok2.URL + `"
`
	_, store1, _, _, srvMgr1 := setupTestEnv(t, allOKYAML)
	router1 := srvMgr1.BuildRouter()

	payload := `{"status":"firing","alerts":[{"status":"firing","labels":{"alertname":"MultiOK","severity":"critical","instance":"app-1"},"annotations":{"summary":"ok"}}]}`
	req1 := httptest.NewRequest(http.MethodPost, "/webhook-alert", strings.NewReader(payload))
	req1.Header.Set("Content-Type", "application/json")
	rec1 := httptest.NewRecorder()
	router1.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("expected 200 when all enabled targets succeed, got %d (%s)", rec1.Code, rec1.Body.String())
	}
	if disabledCalls.Load() != 0 || ok1Calls.Load() != 1 || ok2Calls.Load() != 1 {
		t.Fatalf("unexpected call counts: ok1=%d ok2=%d disabled=%d", ok1Calls.Load(), ok2Calls.Load(), disabledCalls.Load())
	}
	q1 := store1.Query(store.AlertQuery{Page: 1, PageSize: 10})
	if q1.Total != 1 || q1.Items[0].DeliveryState != "sent" || len(q1.Items[0].Deliveries) != 2 {
		t.Fatalf("unexpected stored alert record: %+v", q1)
	}
	if q1.Items[0].Deliveries[0].Target != "告警主群" || q1.Items[0].Deliveries[1].Target != "值班飞书" {
		t.Fatalf("expected Target names in deliveries, got %+v", q1.Items[0].Deliveries)
	}

	// 2. Partial target failure within same channel -> 500 + partial state + Target in store and error body
	partialYAML := `server:
  port: "127.0.0.1:18082"
client:
  - wechat
notifiers:
  wechat:
    webhooks:
      - name: "主群"
        url: "` + ok1.URL + `"
      - name: "备群"
        url: "` + failTarget.URL + `"
      - name: "禁用群"
        url: "` + disabledTarget.URL + `"
        enabled: false
`
	_, store2, _, _, srvMgr2 := setupTestEnv(t, partialYAML)
	router2 := srvMgr2.BuildRouter()

	req2 := httptest.NewRequest(http.MethodPost, "/webhook-alert", strings.NewReader(payload))
	req2.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	router2.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 on partial target failure, got %d (%s)", rec2.Code, rec2.Body.String())
	}
	if !strings.Contains(rec2.Body.String(), "wechat(备群)") {
		t.Fatalf("expected failed target label 'wechat(备群)' in 500 response, got %q", rec2.Body.String())
	}
	if disabledCalls.Load() != 0 {
		t.Fatalf("disabled target must remain skipped, got %d calls", disabledCalls.Load())
	}
	q2 := store2.Query(store.AlertQuery{Page: 1, PageSize: 10})
	if q2.Total != 1 || q2.Items[0].DeliveryState != "partial" || len(q2.Items[0].Deliveries) != 2 {
		t.Fatalf("expected partial alert record with 2 deliveries, got %+v", q2)
	}
	if q2.Items[0].Deliveries[0].Target != "主群" || !q2.Items[0].Deliveries[0].Success {
		t.Fatalf("unexpected delivery[0]: %+v", q2.Items[0].Deliveries[0])
	}
	if q2.Items[0].Deliveries[1].Target != "备群" || q2.Items[0].Deliveries[1].Success {
		t.Fatalf("unexpected delivery[1]: %+v", q2.Items[0].Deliveries[1])
	}
}

func TestMultiWebhookTestNotifierSimulateKafkaAndOverview(t *testing.T) {
	var wc1Calls, wc2Calls, dtCalls, fs1Calls, fs2Calls, disabledCalls atomic.Int32
	wc1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wc1Calls.Add(1)
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	defer wc1.Close()

	wc2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wc2Calls.Add(1)
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	defer wc2.Close()

	dtSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dtCalls.Add(1)
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	defer dtSrv.Close()

	fs1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fs1Calls.Add(1)
		_, _ = w.Write([]byte(`{"code":0,"msg":"success"}`))
	}))
	defer fs1.Close()

	fs2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fs2Calls.Add(1)
		_, _ = w.Write([]byte(`{"code":0,"msg":"success"}`))
	}))
	defer fs2.Close()

	disabledSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		disabledCalls.Add(1)
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	defer disabledSrv.Close()

	kafkaUISrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"pageCount": 1,
			"consumerGroups": [
				{"groupId":"lag-orders","members":2,"topics":1,"state":"STABLE","consumerLag":250000,"coordinator":{"id":1,"host":"k1","port":9092}}
			]
		}`))
	}))
	defer kafkaUISrv.Close()

	initialYAML := `server:
  port: "127.0.0.1:18082"
client:
  - wechat
  - dingtalk
  - feishu
notifiers:
  wechat:
    webhooks:
      - name: "运维群"
        url: "` + wc1.URL + `"
      - name: "值班群"
        url: "` + wc2.URL + `"
      - name: "停用群"
        url: "` + disabledSrv.URL + `"
        enabled: false
  dingtalk:
    webhook_url: "` + dtSrv.URL + `"
  feishu:
    webhooks:
      - name: "飞书一组"
        url: "` + fs1.URL + `"
      - name: "飞书二组"
        url: "` + fs2.URL + `"
kafka_monitor:
  enabled: true
  kafka_ui_url: "` + kafkaUISrv.URL + `"
  cluster_name: "prod-kafka"
  check_interval: 60
  consumer_lag_threshold: 100000
`

	cfgMgr, alertStore, _, svcMgr, srvMgr := setupTestEnv(t, initialYAML)
	svcMgr.InitializeAll(cfgMgr.Get())
	router := srvMgr.BuildRouter()

	doJSON := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	type testNotifierResp struct {
		OK      bool                   `json:"ok"`
		Results []store.DeliveryResult `json:"results"`
	}

	// 1. POST /api/test/notifier: single temporary webhook_url with target
	recSingle := doJSON(http.MethodPost, "/api/test/notifier", fmt.Sprintf(`{"client":"wechat","target":"临时测试群","webhook_url":%q}`, wc1.URL))
	if recSingle.Code != http.StatusOK {
		t.Fatalf("single webhook_url test failed: %d %s", recSingle.Code, recSingle.Body.String())
	}
	var respSingle testNotifierResp
	if err := json.Unmarshal(recSingle.Body.Bytes(), &respSingle); err != nil {
		t.Fatal(err)
	}
	if !respSingle.OK || len(respSingle.Results) != 1 || respSingle.Results[0].Target != "临时测试群" || !respSingle.Results[0].Success {
		t.Fatalf("unexpected single webhook test response: %+v", respSingle)
	}

	// 2. POST /api/test/notifier: temporary webhooks list (skips disabled target)
	tempWebhooksBody := fmt.Sprintf(`{"client":"wechat","webhooks":[{"name":"临时A","url":%q},{"name":"临时禁用","url":%q,"enabled":false},{"url":%q}]}`, wc1.URL, disabledSrv.URL, wc2.URL)
	recList := doJSON(http.MethodPost, "/api/test/notifier", tempWebhooksBody)
	if recList.Code != http.StatusOK {
		t.Fatalf("temporary webhooks test failed: %d %s", recList.Code, recList.Body.String())
	}
	var respList testNotifierResp
	if err := json.Unmarshal(recList.Body.Bytes(), &respList); err != nil {
		t.Fatal(err)
	}
	if !respList.OK || len(respList.Results) != 2 || respList.Results[0].Target != "临时A" || respList.Results[1].Target != "#2" {
		t.Fatalf("unexpected temporary webhooks response: %+v", respList)
	}

	// 3. POST /api/test/notifier: configured multi-webhooks for one channel
	recWechat := doJSON(http.MethodPost, "/api/test/notifier", `{"client":"wechat"}`)
	if recWechat.Code != http.StatusOK {
		t.Fatalf("configured channel test failed: %d %s", recWechat.Code, recWechat.Body.String())
	}
	var respWechat testNotifierResp
	if err := json.Unmarshal(recWechat.Body.Bytes(), &respWechat); err != nil {
		t.Fatal(err)
	}
	if !respWechat.OK || len(respWechat.Results) != 2 || respWechat.Results[0].Target != "运维群" || respWechat.Results[1].Target != "值班群" {
		t.Fatalf("unexpected configured wechat test response: %+v", respWechat)
	}

	// 4. POST /api/test/notifier: client="all" across multiple channels and targets
	recAll := doJSON(http.MethodPost, "/api/test/notifier", `{"client":"all"}`)
	if recAll.Code != http.StatusOK {
		t.Fatalf("client=all test failed: %d %s", recAll.Code, recAll.Body.String())
	}
	var respAll testNotifierResp
	if err := json.Unmarshal(recAll.Body.Bytes(), &respAll); err != nil {
		t.Fatal(err)
	}
	if !respAll.OK || len(respAll.Results) != 5 {
		t.Fatalf("expected 5 results for client=all (2 wechat + 1 dingtalk + 2 feishu), got %+v", respAll)
	}
	expectedAll := []struct {
		client string
		target string
	}{
		{"wechat", "运维群"},
		{"wechat", "值班群"},
		{"dingtalk", ""},
		{"feishu", "飞书一组"},
		{"feishu", "飞书二组"},
	}
	for i, exp := range expectedAll {
		if respAll.Results[i].Client != exp.client || respAll.Results[i].Target != exp.target || !respAll.Results[i].Success {
			t.Fatalf("client=all result[%d] mismatch: expected (%s, %s), got %+v", i, exp.client, exp.target, respAll.Results[i])
		}
	}

	// 5. POST /api/simulate sending to multi-webhook wechat targets
	wc1Before := wc1Calls.Load()
	wc2Before := wc2Calls.Load()
	simRec := doJSON(http.MethodPost, "/api/simulate", `{"status":"firing","alert_name":"SimMulti","severity":"critical","instance":"web-01","summary":"sum","description":"desc","send":true,"clients":["wechat"]}`)
	if simRec.Code != http.StatusOK {
		t.Fatalf("simulate failed: %d %s", simRec.Code, simRec.Body.String())
	}
	var simResp struct {
		Deliveries []store.DeliveryResult `json:"deliveries"`
		Record     *store.AlertRecord     `json:"record"`
	}
	if err := json.Unmarshal(simRec.Body.Bytes(), &simResp); err != nil {
		t.Fatal(err)
	}
	if len(simResp.Deliveries) != 2 || simResp.Deliveries[0].Target != "运维群" || simResp.Deliveries[1].Target != "值班群" {
		t.Fatalf("expected 2 wechat simulate deliveries with targets, got %+v", simResp.Deliveries)
	}
	if simResp.Record == nil || simResp.Record.DeliveryState != "sent" || simResp.Record.Source != "test" {
		t.Fatalf("unexpected simulate record: %+v", simResp.Record)
	}
	if wc1Calls.Load() != wc1Before+1 || wc2Calls.Load() != wc2Before+1 {
		t.Fatalf("expected both wechat targets called on simulate")
	}

	// 6. KafkaMonitorService sending to multiple feishu webhooks
	fs1Before := fs1Calls.Load()
	fs2Before := fs2Calls.Load()
	kafkaStatus, err := svcMgr.TriggerKafkaCheck()
	if err != nil {
		t.Fatalf("TriggerKafkaCheck failed: %v", err)
	}
	if !kafkaStatus.Healthy || kafkaStatus.LaggedLast != 1 || kafkaStatus.TotalAlerts != 1 {
		t.Fatalf("unexpected kafka status: %+v", kafkaStatus)
	}
	if fs1Calls.Load() != fs1Before+1 || fs2Calls.Load() != fs2Before+1 {
		t.Fatalf("expected both feishu targets called by KafkaMonitorService")
	}
	kafkaRecords := alertStore.Query(store.AlertQuery{Source: "kafka"})
	if kafkaRecords.Total != 1 || len(kafkaRecords.Items[0].Deliveries) != 5 {
		t.Fatalf("expected 1 kafka alert with 5 multi-target deliveries, got %+v", kafkaRecords)
	}
	if disabledCalls.Load() != 0 {
		t.Fatalf("disabled webhook must never be called, got %d calls", disabledCalls.Load())
	}

	// 7. PUT /api/config & PUT /api/config/yaml with multi-webhooks followed by GET /api/overview checking configured_count & total_count
	getCfgRec := doJSON(http.MethodGet, "/api/config", "")
	if getCfgRec.Code != http.StatusOK {
		t.Fatalf("GET /api/config failed: %d", getCfgRec.Code)
	}
	var cfgResp struct {
		Config config.AppConfig `json:"config"`
	}
	if err := json.Unmarshal(getCfgRec.Body.Bytes(), &cfgResp); err != nil {
		t.Fatal(err)
	}
	cfgResp.Config.Notifiers["wechat"] = config.NotifierConfig{
		Webhooks: []config.WebhookTarget{
			{Name: "A", URL: wc1.URL},
			{Name: "B", URL: wc2.URL},
			{Name: "C-Disabled", URL: disabledSrv.URL, Enabled: testBoolPtr(false)},
			{Name: "D-Placeholder", URL: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=xxxxxx"},
		},
	}
	putBytes, _ := json.Marshal(cfgResp.Config)
	putRec := doJSON(http.MethodPut, "/api/config", string(putBytes))
	if putRec.Code != http.StatusOK {
		t.Fatalf("PUT /api/config failed: %d %s", putRec.Code, putRec.Body.String())
	}

	ovRec1 := doJSON(http.MethodGet, "/api/overview", "")
	if ovRec1.Code != http.StatusOK {
		t.Fatalf("GET /api/overview failed: %d", ovRec1.Code)
	}
	var ov1 struct {
		Stats store.OverviewStats `json:"stats"`
	}
	if err := json.Unmarshal(ovRec1.Body.Bytes(), &ov1); err != nil {
		t.Fatal(err)
	}
	wcStats1 := ov1.Stats.ByChannel["wechat"]
	if wcStats1.ConfiguredCount != 2 || wcStats1.TotalCount != 4 || !wcStats1.Configured {
		t.Fatalf("expected wechat configured_count=2 total_count=4 after PUT /api/config, got %+v", wcStats1)
	}

	rawYAMLUpdate := fmt.Sprintf(`server:
  port: "127.0.0.1:18082"
client:
  - dingtalk
  - feishu
notifiers:
  wechat:
    webhook_url: ""
  dingtalk:
    webhooks:
      - name: "钉钉1"
        url: %q
      - name: "钉钉2"
        url: %q
      - name: "钉钉停用"
        url: %q
        enabled: false
  feishu:
    webhooks:
      - %q
`, dtSrv.URL, dtSrv.URL+"/2", disabledSrv.URL, fs1.URL)

	yamlBody, _ := json.Marshal(map[string]string{"yaml": rawYAMLUpdate})
	putYAMLRec := doJSON(http.MethodPut, "/api/config/yaml", string(yamlBody))
	if putYAMLRec.Code != http.StatusOK {
		t.Fatalf("PUT /api/config/yaml failed: %d %s", putYAMLRec.Code, putYAMLRec.Body.String())
	}

	ovRec2 := doJSON(http.MethodGet, "/api/overview", "")
	if ovRec2.Code != http.StatusOK {
		t.Fatalf("GET /api/overview after YAML failed: %d", ovRec2.Code)
	}
	var ov2 struct {
		Stats store.OverviewStats `json:"stats"`
	}
	if err := json.Unmarshal(ovRec2.Body.Bytes(), &ov2); err != nil {
		t.Fatal(err)
	}
	if dt := ov2.Stats.ByChannel["dingtalk"]; dt.ConfiguredCount != 2 || dt.TotalCount != 3 || !dt.Enabled {
		t.Fatalf("expected dingtalk configured_count=2 total_count=3 after PUT /api/config/yaml, got %+v", dt)
	}
	if fs := ov2.Stats.ByChannel["feishu"]; fs.ConfiguredCount != 1 || fs.TotalCount != 1 || !fs.Enabled {
		t.Fatalf("expected feishu configured_count=1 total_count=1 after PUT /api/config/yaml, got %+v", fs)
	}
	if wc := ov2.Stats.ByChannel["wechat"]; wc.ConfiguredCount != 0 || wc.TotalCount != 0 || wc.Enabled {
		t.Fatalf("expected wechat configured_count=0 total_count=0 enabled=false, got %+v", wc)
	}

	// 8. POST /api/test/notifier when client="all" has one failing target -> ok=false
	failHookSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errcode":310000,"errmsg":"sign invalid"}`))
	}))
	defer failHookSrv.Close()

	failYAML := fmt.Sprintf(`server:
  port: "127.0.0.1:18082"
client:
  - dingtalk
  - feishu
notifiers:
  dingtalk:
    webhooks:
      - name: "正常钉钉"
        url: %q
      - name: "故障钉钉"
        url: %q
  feishu:
    webhooks:
      - %q
`, dtSrv.URL, failHookSrv.URL, fs1.URL)
	failYAMLBody, _ := json.Marshal(map[string]string{"yaml": failYAML})
	if putFailRec := doJSON(http.MethodPut, "/api/config/yaml", string(failYAMLBody)); putFailRec.Code != http.StatusOK {
		t.Fatalf("PUT /api/config/yaml for partial test failed: %d %s", putFailRec.Code, putFailRec.Body.String())
	}
	recAllPartial := doJSON(http.MethodPost, "/api/test/notifier", `{"client":"all"}`)
	if recAllPartial.Code != http.StatusOK {
		t.Fatalf("client=all partial test failed: %d", recAllPartial.Code)
	}
	var respAllPartial testNotifierResp
	if err := json.Unmarshal(recAllPartial.Body.Bytes(), &respAllPartial); err != nil {
		t.Fatal(err)
	}
	if respAllPartial.OK || len(respAllPartial.Results) != 3 {
		t.Fatalf("expected ok=false with 3 results, got %+v", respAllPartial)
	}
	if !respAllPartial.Results[0].Success || respAllPartial.Results[1].Success || respAllPartial.Results[1].Target != "故障钉钉" || !respAllPartial.Results[2].Success {
		t.Fatalf("unexpected partial test ordering/outcomes: %+v", respAllPartial.Results)
	}
}

func TestAuthEndpointsWithoutTokenConfigured(t *testing.T) {
	_, _, _, _, srvMgr := setupTestEnv(t, "")
	router := srvMgr.BuildRouter()

	stReq := httptest.NewRequest(http.MethodGet, "/api/auth/status", nil)
	stRec := httptest.NewRecorder()
	router.ServeHTTP(stRec, stReq)
	if stRec.Code != http.StatusOK || !strings.Contains(stRec.Body.String(), `"auth_required":false`) || !strings.Contains(stRec.Body.String(), `"authenticated":true`) {
		t.Fatalf("unexpected unconfigured /api/auth/status: %d %s", stRec.Code, stRec.Body.String())
	}

	lgReq := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"api_key":""}`))
	lgReq.Header.Set("Content-Type", "application/json")
	lgRec := httptest.NewRecorder()
	router.ServeHTTP(lgRec, lgReq)
	if lgRec.Code != http.StatusOK || !strings.Contains(lgRec.Body.String(), `"authenticated":true`) {
		t.Fatalf("unexpected unconfigured /api/auth/login: %d %s", lgRec.Code, lgRec.Body.String())
	}
}

func TestWeChatRateLimitEarlyStopCooldownAndTestBypass(t *testing.T) {
	ResetRateLimitCooldown()
	SetRateLimitCooldownDuration(10 * time.Second)
	t.Cleanup(func() {
		ResetRateLimitCooldown()
		SetRateLimitCooldownDuration(defaultRateLimitCooldownWindow)
	})

	var calls atomic.Int32
	wechatSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 2 {
			_, _ = w.Write([]byte(`{"errcode":45009,"errmsg":"api freq out of limit"}`))
			return
		}
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	defer wechatSrv.Close()

	yamlCfg := `server:
  port: "127.0.0.1:18082"
client:
  - wechat
notifiers:
  wechat:
    webhooks:
      - name: "企微测试群"
        url: "` + wechatSrv.URL + `"
`
	_, alertStore, _, _, srvMgr := setupTestEnv(t, yamlCfg)
	t.Cleanup(func() {
		_ = srvMgr.Shutdown()
	})
	router := srvMgr.BuildRouter()

	longDesc := strings.Repeat("x", 1500)
	alerts := make([]map[string]interface{}, 0, 5)
	for i := 0; i < 5; i++ {
		alerts = append(alerts, map[string]interface{}{
			"status": "firing",
			"labels": map[string]string{
				"alertname": fmt.Sprintf("StormBatch-%d", i),
				"severity":  "warning",
				"instance":  "node-1",
			},
			"annotations": map[string]string{
				"summary":     "summary",
				"description": longDesc,
			},
		})
	}
	payloadBytes, _ := json.Marshal(map[string]interface{}{
		"status": "firing",
		"alerts": alerts,
	})

	// 1. First webhook hits 45009 on batch 2 -> stops remaining batches, enters cooldown, returns 202 (no retry storm).
	req1 := httptest.NewRequest(http.MethodPost, "/webhook-alert", bytes.NewReader(payloadBytes))
	req1.Header.Set("Content-Type", "application/json")
	rec1 := httptest.NewRecorder()
	router.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted on pure rate-limit failure, got %d (%s)", rec1.Code, rec1.Body.String())
	}
	if calls.Load() != 2 {
		t.Fatalf("expected early stop after batch 2 (2 HTTP calls), got %d", calls.Load())
	}
	stored := alertStore.Query(store.AlertQuery{Page: 1, PageSize: 10})
	if stored.Total != 5 || len(stored.Items[0].Deliveries) != 1 || !stored.Items[0].Deliveries[0].RateLimited {
		t.Fatalf("expected 5 records marked RateLimited=true, got %+v", stored)
	}

	// 2. Second webhook during cooldown skips HTTP call completely and still returns 202.
	req2 := httptest.NewRequest(http.MethodPost, "/webhook-alert", bytes.NewReader(payloadBytes))
	req2.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	router.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted during cooldown, got %d (%s)", rec2.Code, rec2.Body.String())
	}
	if calls.Load() != 2 {
		t.Fatalf("expected 0 extra HTTP calls during cooldown, total calls=%d", calls.Load())
	}

	// 3. Manual /api/test/notifier must bypass alert rate-limit cooldown.
	testReq := httptest.NewRequest(http.MethodPost, "/api/test/notifier", strings.NewReader(`{"client":"wechat"}`))
	testReq.Header.Set("Content-Type", "application/json")
	testRec := httptest.NewRecorder()
	router.ServeHTTP(testRec, testReq)
	if testRec.Code != http.StatusOK || !strings.Contains(testRec.Body.String(), `"ok":true`) {
		t.Fatalf("expected /api/test/notifier to bypass cooldown and succeed, got %d %s", testRec.Code, testRec.Body.String())
	}
	if calls.Load() != 3 {
		t.Fatalf("expected /api/test/notifier to reach mock server (calls=3), got %d", calls.Load())
	}
}

func TestWebhookStormDoesNotBlockControlPlane(t *testing.T) {
	ResetRateLimitCooldown()
	t.Cleanup(ResetRateLimitCooldown)

	releaseWechat := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() { close(releaseWechat) })
	}

	var wechatCalls atomic.Int32
	slowWechat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wechatCalls.Add(1)
		select {
		case <-releaseWechat:
		case <-r.Context().Done():
		}
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	defer slowWechat.Close()
	defer release()

	yamlCfg := `server:
  port: "127.0.0.1:18082"
client:
  - wechat
notifiers:
  wechat:
    webhooks:
      - name: "压测群"
        url: "` + slowWechat.URL + `"
`
	cfgMgr, _, _, svcMgr, srvMgr := setupTestEnv(t, yamlCfg)
	cfgMgr.OnReload(func(c *config.AppConfig) {
		svcMgr.ReloadAsync(c)
	})
	router := srvMgr.BuildRouter()
	t.Cleanup(func() {
		release()
		_ = srvMgr.Shutdown()
	})

	alerts := make([]map[string]interface{}, 0, 200)
	for i := 0; i < 200; i++ {
		alerts = append(alerts, map[string]interface{}{
			"status": "firing",
			"labels": map[string]string{
				"alertname": "KubePodNotReady",
				"severity":  "warning",
				"instance":  fmt.Sprintf("pod-%d", i),
			},
			"annotations": map[string]string{
				"summary":     "Pod not ready",
				"description": fmt.Sprintf("Pod syncplant/pod-%d non-ready", i),
			},
		})
	}
	stormBody, _ := json.Marshal(map[string]interface{}{
		"status": "firing",
		"alerts": alerts,
	})

	var acceptedCount atomic.Int32
	var okCount atomic.Int32
	var stormWG sync.WaitGroup
	for i := 0; i < 30; i++ {
		stormWG.Add(1)
		go func() {
			defer stormWG.Done()
			req := httptest.NewRequest(http.MethodPost, "/webhook-alert", bytes.NewReader(stormBody))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			switch rec.Code {
			case http.StatusOK:
				okCount.Add(1)
			case http.StatusAccepted:
				acceptedCount.Add(1)
			default:
				t.Errorf("unexpected webhook storm status %d: %s", rec.Code, rec.Body.String())
			}
		}()
	}

	// Deterministically wait until sync concurrency slots are saturated and overflow enters async queue (202).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if acceptedCount.Load() > 0 && wechatCalls.Load() >= defaultMaxSyncWebhookConcurrency {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if acceptedCount.Load() == 0 {
		release()
		stormWG.Wait()
		t.Fatalf("timed out waiting for overflowed storm requests to enter async queue (202): ok=%d accepted=%d wechatCalls=%d",
			okCount.Load(), acceptedCount.Load(), wechatCalls.Load())
	}

	// Hit control plane endpoints while all sync + async delivery slots remain blocked.
	var ctrlWG sync.WaitGroup
	ctrlWG.Add(4)

	go func() {
		defer ctrlWG.Done()
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("GET /healthz failed during storm: %d", rec.Code)
		}
	}()

	go func() {
		defer ctrlWG.Done()
		req := httptest.NewRequest(http.MethodGet, "/api/alerts?page=1&page_size=20", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("GET /api/alerts failed during storm: %d", rec.Code)
		}
	}()

	go func() {
		defer ctrlWG.Done()
		cfg := cfgMgr.Get()
		cfg.Clients = []string{}
		body, _ := json.Marshal(cfg)
		req := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("PUT /api/config failed during storm: %d %s", rec.Code, rec.Body.String())
		}
	}()

	go func() {
		defer ctrlWG.Done()
		req := httptest.NewRequest(http.MethodDelete, "/api/alerts", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("DELETE /api/alerts failed during storm: %d %s", rec.Code, rec.Body.String())
		}
	}()

	ctrlWG.Wait()
	release()
	stormWG.Wait()
	_ = srvMgr.Shutdown()

	if acceptedCount.Load() == 0 {
		t.Fatalf("expected overflowed storm requests to enter async queue (202), got ok=%d accepted=%d", okCount.Load(), acceptedCount.Load())
	}
	if wechatCalls.Load() < defaultMaxSyncWebhookConcurrency {
		t.Fatalf("expected at least %d WeChat calls during saturation, got %d", defaultMaxSyncWebhookConcurrency, wechatCalls.Load())
	}
}

func TestServiceManagerSelectiveReloadAndReloadAsyncShutdown(t *testing.T) {
	kafkaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"pageCount":1,"consumerGroups":[]}`))
	}))
	defer kafkaSrv.Close()

	yamlCfg := `server:
  port: "127.0.0.1:18082"
client:
  - wechat
notifiers:
  wechat:
    webhook_url: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=initial"
kafka_monitor:
  enabled: true
  kafka_ui_url: "` + kafkaSrv.URL + `"
  cluster_name: "cluster-a"
  check_interval: 60
  consumer_lag_threshold: 100
`
	cfgMgr, _, _, svcMgr, _ := setupTestEnv(t, yamlCfg)
	svcMgr.InitializeAll(cfgMgr.Get())

	svcMgr.mu.Lock()
	firstKafka := svcMgr.kafkaMonitorService
	svcMgr.mu.Unlock()
	if firstKafka == nil {
		t.Fatal("expected kafkaMonitorService started")
	}

	// Changing only notifier/filter must NOT restart KafkaMonitorService.
	cfg1 := cfgMgr.Get()
	cfg1.Clients = []string{}
	cfg1.Filter.AlertName.Exclude = []string{"Noise*"}
	svcMgr.Reload(cfg1)

	svcMgr.mu.Lock()
	afterNotifierChange := svcMgr.kafkaMonitorService
	svcMgr.mu.Unlock()
	if afterNotifierChange != firstKafka {
		t.Fatal("expected KafkaMonitorService instance preserved when only notifier/filter changed")
	}

	// Changing Kafka cluster name MUST restart KafkaMonitorService.
	cfg2 := cfgMgr.Get()
	cfg2.KafkaMonitor.ClusterName = "cluster-b"
	svcMgr.Reload(cfg2)

	svcMgr.mu.Lock()
	afterKafkaChange := svcMgr.kafkaMonitorService
	svcMgr.mu.Unlock()
	if afterKafkaChange == nil || afterKafkaChange == firstKafka {
		t.Fatal("expected KafkaMonitorService restarted when kafka_monitor config changed")
	}

	// Rapid concurrent ReloadAsync followed by Shutdown must complete without races or leaks.
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			c := cfgMgr.Get()
			c.KafkaMonitor.ClusterName = fmt.Sprintf("cluster-%d", idx)
			svcMgr.ReloadAsync(c)
		}(i)
	}
	wg.Wait()
	svcMgr.Shutdown()
}

func TestAlertmanagerWebhookBodyLimit413(t *testing.T) {
	_, _, _, _, srvMgr := setupTestEnv(t, "")
	t.Cleanup(func() {
		_ = srvMgr.Shutdown()
	})
	router := srvMgr.BuildRouter()

	var payload bytes.Buffer
	payload.WriteString(`{"status":"firing","padding":"`)
	payload.Write(bytes.Repeat([]byte("a"), int(maxWebhookBodyBytes)+1024))
	payload.WriteString(`"}`)

	// Test streaming/chunked body (ContentLength = -1) rejected via http.MaxBytesReader.
	req := httptest.NewRequest(http.MethodPost, "/webhook-alert", bytes.NewReader(payload.Bytes()))
	req.ContentLength = -1
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 Request Entity Too Large for >10MB body, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestControlPlanePOSTAliasesAndAuth(t *testing.T) {
	yamlCfg := `server:
  port: "127.0.0.1:18082"
  auth_token: "post-alias-token"
client: []
notifiers: {}
`
	_, alertStore, _, _, srvMgr := setupTestEnv(t, yamlCfg)
	t.Cleanup(func() {
		_ = srvMgr.Shutdown()
	})
	router := srvMgr.BuildRouter()

	alertStore.Add(store.AlertRecord{
		Source:        "alertmanager",
		Status:        "firing",
		DeliveryState: "sent",
		AlertName:     "AliasTestAlert",
		Severity:      "warning",
	})

	// 1. Unauthenticated POST alias requests must be rejected with 401.
	for _, tc := range []struct {
		path string
		body string
	}{
		{"/api/alerts/clear", ""},
		{"/api/config", `{}`},
		{"/api/config/yaml", `{"yaml":""}`},
	} {
		req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
		if tc.body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("POST %s without token expected 401, got %d", tc.path, rec.Code)
		}
	}

	doAuthedPOST := func(path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("X-API-Key", "post-alias-token")
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	// 2. POST /api/alerts/clear clears AlertStore just like DELETE /api/alerts.
	clearRec := doAuthedPOST("/api/alerts/clear", "")
	if clearRec.Code != http.StatusOK || alertStore.Query(store.AlertQuery{}).Total != 0 {
		t.Fatalf("POST /api/alerts/clear failed: %d %s", clearRec.Code, clearRec.Body.String())
	}

	// 3. POST /api/config saves config just like PUT /api/config.
	getReq := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	getReq.Header.Set("X-API-Key", "post-alias-token")
	getRec := httptest.NewRecorder()
	router.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET /api/config failed: %d", getRec.Code)
	}
	var cfgResp struct {
		Config config.AppConfig `json:"config"`
	}
	if err := json.Unmarshal(getRec.Body.Bytes(), &cfgResp); err != nil {
		t.Fatal(err)
	}
	cfgResp.Config.Clients = []string{"wechat"}
	cfgResp.Config.Notifiers["wechat"] = config.NotifierConfig{
		Webhooks: []config.WebhookTarget{
			{Name: "测试群", URL: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=post-test-123"},
		},
	}
	cfgBytes, _ := json.Marshal(cfgResp.Config)
	postCfgRec := doAuthedPOST("/api/config", string(cfgBytes))
	if postCfgRec.Code != http.StatusOK || !strings.Contains(postCfgRec.Body.String(), "key=post-test-123") {
		t.Fatalf("POST /api/config failed: %d %s", postCfgRec.Code, postCfgRec.Body.String())
	}

	// 4. POST /api/config/yaml saves raw YAML just like PUT /api/config/yaml.
	yamlBody, _ := json.Marshal(map[string]string{
		"yaml": "server:\n  port: \"127.0.0.1:18082\"\n  auth_token: \"post-alias-token\"\nclient:\n  - dingtalk\nnotifiers:\n  dingtalk:\n    webhook_url: \"https://oapi.dingtalk.com/robot/send?access_token=post-dt-123\"\n",
	})
	postYAMLRec := doAuthedPOST("/api/config/yaml", string(yamlBody))
	if postYAMLRec.Code != http.StatusOK || !strings.Contains(postYAMLRec.Body.String(), `"dingtalk"`) {
		t.Fatalf("POST /api/config/yaml failed: %d %s", postYAMLRec.Code, postYAMLRec.Body.String())
	}
}
