package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v2"
)

func TestNewConfigManagerCreatesDefaultFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "nested", "config.yaml")

	mgr, err := NewConfigManager(cfgPath)
	if err != nil {
		t.Fatalf("NewConfigManager failed: %v", err)
	}
	if _, err := os.Stat(cfgPath); err != nil {
		t.Fatalf("expected default config file to be created at %s: %v", cfgPath, err)
	}

	cfg := mgr.Get()
	if cfg.Server.Port != "0.0.0.0:18082" {
		t.Fatalf("unexpected default port: %s", cfg.Server.Port)
	}
	if !cfg.Server.IsUIEnabled() {
		t.Fatal("expected ui_enabled to default to true")
	}
	if len(cfg.Clients) != 0 {
		t.Fatalf("expected 0 default clients, got %v", cfg.Clients)
	}
	if cfg.TrafficAlert.Enabled || cfg.KafkaMonitor.Enabled {
		t.Fatal("expected traffic_alert and kafka_monitor disabled by default")
	}
	if cfg.HasConfiguredClient() {
		t.Fatal("expected HasConfiguredClient false on fresh default config")
	}
}

func TestEnvOverridesAndAutoClientPopulation(t *testing.T) {
	t.Run("auto-adds client when config client list is empty", func(t *testing.T) {
		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "config.yaml")

		t.Setenv("ALERT_WEBHOOK_PORT", "127.0.0.1:19090")
		t.Setenv("ALERT_WEBHOOK_AUTH_TOKEN", "secret-token")
		t.Setenv("ALERT_WEBHOOK_WECHAT_URL", "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=realkey123")
		t.Setenv("ALERT_WEBHOOK_CLICKHOUSE_HOST", "ch.internal")
		t.Setenv("ALERT_WEBHOOK_CLICKHOUSE_PORT", "9001")
		t.Setenv("ALERT_WEBHOOK_CLICKHOUSE_DB", "prod_logs")
		t.Setenv("ALERT_WEBHOOK_TRAFFIC_ENABLED", "true")
		t.Setenv("ALERT_WEBHOOK_KAFKA_ENABLED", "true")
		t.Setenv("ALERT_WEBHOOK_KAFKA_UI_URL", "http://kafka-ui:8080")
		t.Setenv("ALERT_WEBHOOK_KAFKA_CLUSTER", "prod-kafka")

		mgr, err := NewConfigManager(cfgPath)
		if err != nil {
			t.Fatalf("NewConfigManager failed: %v", err)
		}

		cfg := mgr.Get()
		if cfg.Server.Port != "127.0.0.1:19090" {
			t.Fatalf("port override failed: %s", cfg.Server.Port)
		}
		if cfg.Server.AuthToken != "secret-token" {
			t.Fatalf("auth token override failed: %s", cfg.Server.AuthToken)
		}
		if len(cfg.Clients) != 1 || cfg.Clients[0] != "wechat" {
			t.Fatalf("expected wechat auto-added to empty client list, got %v", cfg.Clients)
		}
		if !cfg.HasConfiguredClient() {
			t.Fatal("expected HasConfiguredClient true with env wechat URL")
		}
		if cfg.ClickHouse.Host != "ch.internal" || cfg.ClickHouse.Port != 9001 || cfg.ClickHouse.Database != "prod_logs" {
			t.Fatalf("clickhouse env override mismatch: %+v", cfg.ClickHouse)
		}
		if !cfg.TrafficAlert.Enabled || !cfg.KafkaMonitor.Enabled || cfg.KafkaMonitor.ClusterName != "prod-kafka" {
			t.Fatalf("monitor env override mismatch: traffic=%+v kafka=%+v", cfg.TrafficAlert, cfg.KafkaMonitor)
		}
		if len(mgr.EnvOverrides()) < 8 {
			t.Fatalf("expected env overrides list populated, got %v", mgr.EnvOverrides())
		}
	})

	t.Run("does not override explicitly populated client list", func(t *testing.T) {
		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "config.yaml")
		initialYAML := `server:
  port: "0.0.0.0:18082"
client:
  - dingtalk
notifiers:
  dingtalk:
    webhook_url: "https://oapi.dingtalk.com/robot/send?access_token=realdingtoken"
`
		if err := os.WriteFile(cfgPath, []byte(initialYAML), 0644); err != nil {
			t.Fatal(err)
		}

		t.Setenv("ALERT_WEBHOOK_WECHAT_URL", "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=realkey123")
		mgr, err := NewConfigManager(cfgPath)
		if err != nil {
			t.Fatalf("NewConfigManager failed: %v", err)
		}

		cfg := mgr.Get()
		if len(cfg.Clients) != 1 || cfg.Clients[0] != "dingtalk" {
			t.Fatalf("expected explicit client list [dingtalk] preserved, got %v", cfg.Clients)
		}
		if cfg.Notifiers["wechat"].WebhookURL == "" {
			t.Fatal("expected wechat notifier URL still populated from env")
		}
	})
}

func TestConfigValidationAndAtomicUpdateReload(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	mgr, err := NewConfigManager(cfgPath)
	if err != nil {
		t.Fatalf("NewConfigManager failed: %v", err)
	}

	var reloadCalls int
	var mu sync.Mutex
	mgr.OnReload(func(c *AppConfig) {
		mu.Lock()
		reloadCalls++
		mu.Unlock()
	})

	// Invalid ClickHouse database identifier must fail validation
	badCfg := mgr.Get()
	badCfg.ClickHouse.Database = "nginxlogs; DROP TABLE x"
	if _, _, err := mgr.Update(badCfg); err == nil {
		t.Fatal("expected validation error for unsafe clickhouse database name")
	} else {
		var valErr *ValidationError
		if !errors.As(err, &valErr) {
			t.Fatalf("expected ValidationError, got %T", err)
		}
	}

	// Invalid client name must fail validation
	badClientCfg := mgr.Get()
	badClientCfg.Clients = []string{"slack"}
	if _, _, err := mgr.Update(badClientCfg); err == nil {
		t.Fatal("expected validation error for unsupported client")
	}

	// Valid update saves atomically and triggers reload callback
	goodCfg := mgr.Get()
	goodCfg.Clients = []string{"feishu"}
	goodCfg.Notifiers["feishu"] = NotifierConfig{WebhookURL: "https://open.feishu.cn/open-apis/bot/v2/hook/abc123"}
	goodCfg.Server.Port = "0.0.0.0:19999"

	warnings, restartRequired, err := mgr.Update(goodCfg)
	if err != nil {
		t.Fatalf("expected valid update to succeed, got %v (warnings=%+v)", err, warnings)
	}
	if !restartRequired {
		t.Fatal("expected restartRequired=true when port changes")
	}

	// Mutating returned Get() copy must not affect stored config
	mutated := mgr.Get()
	mutated.Clients[0] = "tampered"
	if mgr.Get().Clients[0] != "feishu" {
		t.Fatal("Get() did not return an isolated deep copy")
	}

	// UpdateYAML test
	yamlPayload := `server:
  port: "0.0.0.0:19999"
client:
  - wechat
notifiers:
  wechat:
    webhook_url: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=validkey"
`
	_, restart2, err := mgr.UpdateYAML(yamlPayload)
	if err != nil {
		t.Fatalf("UpdateYAML failed: %v", err)
	}
	if restart2 {
		t.Fatal("expected restartRequired=false when port is unchanged")
	}
	if mgr.Get().Clients[0] != "wechat" {
		t.Fatalf("expected client wechat after UpdateYAML, got %v", mgr.Get().Clients)
	}

	// Reload from disk
	if _, err := mgr.Reload(); err != nil {
		t.Fatalf("Reload failed: %v", err)
	}

	mu.Lock()
	if reloadCalls != 3 {
		t.Fatalf("expected 3 OnReload invocations, got %d", reloadCalls)
	}
	mu.Unlock()
}

func TestEvaluateAlertFilterAndWildcards(t *testing.T) {
	cfg := DefaultAppConfig()
	cfg.Filter = AlertFilter{
		AlertName: AlertNameFilter{
			Include: []string{"High*CPU", "*Memory*"},
			Exclude: []string{"*Test*", "HighDebugCPU"},
		},
		Severity: SeverityFilter{
			Include: []string{"critical", "warning"},
			Exclude: []string{"info", "none"},
		},
	}

	cases := []struct {
		name     string
		severity string
		allowed  bool
	}{
		{"HighUsageCPU", "critical", true},
		{"NodeMemoryHigh", "warning", true},
		{"HighTestCPU", "critical", false},    // excluded by *Test*
		{"HighDebugCPU", "critical", false},   // excluded by exact match
		{"DiskSpaceLow", "critical", false},   // not in include
		{"HighUsageCPU", "info", false},       // excluded severity
		{"HighUsageCPU", "emergency", false},  // not in severity include
		{"NodeMemoryHigh", "critical", true},
	}

	for _, tc := range cases {
		dec := cfg.EvaluateAlertFilter(tc.name, tc.severity)
		if dec.Allowed != tc.allowed {
			t.Errorf("EvaluateAlertFilter(%q, %q) allowed=%v (reason=%s), want %v",
				tc.name, tc.severity, dec.Allowed, dec.Reason, tc.allowed)
		}
		if cfg.ShouldSendAlert(tc.name, tc.severity) != tc.allowed {
			t.Errorf("ShouldSendAlert(%q, %q) mismatch", tc.name, tc.severity)
		}
	}

	// Multi-wildcard pattern *a*b*
	if !matchWildcard("xaybz", "*a*b*") {
		t.Fatal("expected *a*b* to match xaybz")
	}
	if matchWildcard("xayz", "*a*b*") {
		t.Fatal("expected *a*b* not to match xayz")
	}
}

func TestIsConfiguredWebhookURL(t *testing.T) {
	if IsConfiguredWebhookURL("") {
		t.Fatal("empty URL should not be configured")
	}
	if IsConfiguredWebhookURL("https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=xxxxxxxxxxxxxxxxxxxxxx") {
		t.Fatal("placeholder URL should not be configured")
	}
	if IsConfiguredWebhookURL("https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=") {
		t.Fatal("trailing empty key URL should not be configured")
	}
	if !IsConfiguredWebhookURL("https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=real-key-123") {
		t.Fatal("valid URL should be configured")
	}
}

func TestConfigManagerConcurrentAccess(t *testing.T) {
	dir := t.TempDir()
	mgr, err := NewConfigManager(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				_ = mgr.Get()
				_ = mgr.RawYAML()
				_ = mgr.EnvOverrides()
				if idx%2 == 0 && j%5 == 0 {
					c := mgr.Get()
					c.TrafficAlert.CountThreshold = j + 1
					_, _, _ = mgr.Update(c)
				}
			}
		}(i)
	}
	wg.Wait()
}

func TestMultiWebhookYAMLJSONRoundtripAndClone(t *testing.T) {
	t.Run("legacy single webhook_url YAML and JSON roundtrip", func(t *testing.T) {
		legacyYAML := `client:
  - wechat
notifiers:
  wechat:
    webhook_url: "  https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=legacy123  "
`
		var cfg AppConfig
		if err := yaml.Unmarshal([]byte(legacyYAML), &cfg); err != nil {
			t.Fatalf("yaml.Unmarshal failed: %v", err)
		}
		cfg.ApplyDefaults()

		wc := cfg.Notifiers["wechat"]
		if wc.WebhookURL != "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=legacy123" {
			t.Fatalf("unexpected WebhookURL: %q", wc.WebhookURL)
		}
		if len(wc.Webhooks) != 1 || wc.Webhooks[0].URL != wc.WebhookURL || !wc.Webhooks[0].IsEnabled() {
			t.Fatalf("expected normalized single target, got %+v", wc.Webhooks)
		}

		marshaledYAML, err := yaml.Marshal(&cfg)
		if err != nil {
			t.Fatalf("yaml.Marshal failed: %v", err)
		}
		yamlStr := string(marshaledYAML)
		if !strings.Contains(yamlStr, "webhook_url: https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=legacy123") {
			t.Fatalf("expected legacy single unnamed enabled target to marshal as webhook_url, got:\n%s", yamlStr)
		}
		if strings.Contains(yamlStr, "webhooks:") {
			t.Fatalf("did not expect webhooks key for legacy single unnamed target, got:\n%s", yamlStr)
		}

		// JSON roundtrip
		rawJSON, err := json.Marshal(&cfg)
		if err != nil {
			t.Fatalf("json.Marshal failed: %v", err)
		}
		var fromJSON AppConfig
		if err := json.Unmarshal(rawJSON, &fromJSON); err != nil {
			t.Fatalf("json.Unmarshal failed: %v", err)
		}
		fromJSON.ApplyDefaults()
		if !fromJSON.HasConfiguredClient() || fromJSON.Notifiers["wechat"].PrimaryURL() != wc.WebhookURL {
			t.Fatalf("JSON roundtrip mismatch: %+v", fromJSON.Notifiers["wechat"])
		}
	})

	t.Run("multi-webhook YAML/JSON shorthand and objects, MarshalYAML without redundant webhook_url, deep Clone", func(t *testing.T) {
		multiYAML := `client:
  - wechat
  - dingtalk
notifiers:
  wechat:
    webhooks:
      - name: " 运维群 "
        url: " https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=k1 "
      - " https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=k2 "
      - name: "备份群"
        url: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=k3"
        enabled: false
  dingtalk:
    webhooks:
      - name: "唯一命名群"
        url: "https://oapi.dingtalk.com/robot/send?access_token=dt1"
`
		var cfg AppConfig
		if err := yaml.Unmarshal([]byte(multiYAML), &cfg); err != nil {
			t.Fatalf("yaml.Unmarshal failed: %v", err)
		}
		cfg.ApplyDefaults()

		wc := cfg.Notifiers["wechat"]
		if len(wc.Webhooks) != 3 {
			t.Fatalf("expected 3 wechat targets, got %d", len(wc.Webhooks))
		}
		if wc.Webhooks[0].Name != "运维群" || wc.Webhooks[0].URL != "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=k1" {
			t.Fatalf("unexpected target[0]: %+v", wc.Webhooks[0])
		}
		if wc.Webhooks[1].Name != "" || wc.Webhooks[1].URL != "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=k2" || !wc.Webhooks[1].IsEnabled() {
			t.Fatalf("unexpected shorthand target[1]: %+v", wc.Webhooks[1])
		}
		if wc.Webhooks[2].IsEnabled() {
			t.Fatalf("expected target[2] disabled, got %+v", wc.Webhooks[2])
		}
		if len(wc.EnabledTargets()) != 2 || len(wc.ConfiguredTargets()) != 2 {
			t.Fatalf("expected 2 enabled & configured wechat targets, got enabled=%d configured=%d",
				len(wc.EnabledTargets()), len(wc.ConfiguredTargets()))
		}
		if wc.PrimaryURL() != "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=k1" {
			t.Fatalf("unexpected PrimaryURL: %q", wc.PrimaryURL())
		}

		// MarshalYAML must omit redundant webhook_url when webhooks list is used or single named target
		outYAML, err := yaml.Marshal(&cfg)
		if err != nil {
			t.Fatalf("yaml.Marshal failed: %v", err)
		}
		var rawMap map[string]interface{}
		if err := yaml.Unmarshal(outYAML, &rawMap); err != nil {
			t.Fatalf("re-unmarshal raw map failed: %v", err)
		}
		notifiersMap := rawMap["notifiers"].(map[interface{}]interface{})
		wechatMap := notifiersMap["wechat"].(map[interface{}]interface{})
		if _, hasLegacyKey := wechatMap["webhook_url"]; hasLegacyKey {
			t.Fatalf("expected no redundant webhook_url in marshaled multi-webhook YAML:\n%s", string(outYAML))
		}
		dingtalkMap := notifiersMap["dingtalk"].(map[interface{}]interface{})
		if _, hasLegacyKey := dingtalkMap["webhook_url"]; hasLegacyKey {
			t.Fatalf("expected no redundant webhook_url for single named target:\n%s", string(outYAML))
		}

		// Roundtrip back from marshaled YAML
		var roundtrip AppConfig
		if err := yaml.Unmarshal(outYAML, &roundtrip); err != nil {
			t.Fatalf("yaml.Unmarshal roundtrip failed: %v", err)
		}
		roundtrip.ApplyDefaults()
		if len(roundtrip.Notifiers["wechat"].AllTargets()) != 3 {
			t.Fatalf("expected 3 targets after YAML roundtrip, got %+v", roundtrip.Notifiers["wechat"].AllTargets())
		}

		// JSON shorthand + object unmarshal
		jsonPayload := `{
			"client": ["feishu"],
			"notifiers": {
				"feishu": {
					"webhooks": [
						" https://open.feishu.cn/open-apis/bot/v2/hook/f1 ",
						{"name": " 研发群 ", "url": " https://open.feishu.cn/open-apis/bot/v2/hook/f2 ", "enabled": false}
					]
				}
			}
		}`
		var jCfg AppConfig
		if err := json.Unmarshal([]byte(jsonPayload), &jCfg); err != nil {
			t.Fatalf("json.Unmarshal failed: %v", err)
		}
		jCfg.ApplyDefaults()
		fs := jCfg.Notifiers["feishu"]
		if len(fs.Webhooks) != 2 || fs.Webhooks[0].URL != "https://open.feishu.cn/open-apis/bot/v2/hook/f1" {
			t.Fatalf("unexpected feishu JSON targets: %+v", fs.Webhooks)
		}
		if fs.Webhooks[1].Name != "研发群" || fs.Webhooks[1].IsEnabled() {
			t.Fatalf("unexpected feishu JSON target[1]: %+v", fs.Webhooks[1])
		}

		// Deep Clone isolation of Webhooks slice and Enabled pointers
		cloned := cfg.Clone()
		cloned.Notifiers["wechat"].Webhooks[0].URL = "https://mutated.example.com"
		*cloned.Notifiers["wechat"].Webhooks[2].Enabled = true
		if cfg.Notifiers["wechat"].Webhooks[0].URL != "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=k1" {
			t.Fatal("Clone did not isolate Webhooks slice")
		}
		if *cfg.Notifiers["wechat"].Webhooks[2].Enabled != false {
			t.Fatal("Clone did not deep-copy Enabled *bool pointer")
		}

		targetsMap := cfg.NotifierTargetsMap()
		*targetsMap["wechat"][2].Enabled = true
		if *cfg.Notifiers["wechat"].Webhooks[2].Enabled != false {
			t.Fatal("NotifierTargetsMap did not deep-copy Enabled *bool pointer")
		}

		// FormatWebhookTargetLabel checks
		if got := FormatWebhookTargetLabel(WebhookTarget{Name: " 运维群 "}, 0, 1); got != "运维群" {
			t.Fatalf("expected 运维群, got %q", got)
		}
		if got := FormatWebhookTargetLabel(WebhookTarget{}, 1, 2); got != "#2" {
			t.Fatalf("expected #2, got %q", got)
		}
		if got := FormatWebhookTargetLabel(WebhookTarget{}, 0, 1); got != "" {
			t.Fatalf("expected empty label for single unnamed target, got %q", got)
		}
	})
}

func TestClearingAllWebhooksDoesNotResurrectOldURL(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	mgr, err := NewConfigManager(cfgPath)
	if err != nil {
		t.Fatalf("NewConfigManager failed: %v", err)
	}

	// First configure a wechat webhook
	cfg := mgr.Get()
	cfg.Clients = []string{"wechat"}
	cfg.Notifiers["wechat"] = NotifierConfig{
		WebhookURL: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=oldkey",
	}
	if _, _, err := mgr.Update(cfg); err != nil {
		t.Fatalf("initial update failed: %v", err)
	}

	// Now clear all webhooks on wechat
	cleared := mgr.Get()
	cleared.Notifiers["wechat"] = NotifierConfig{
		WebhookURL: "",
		Webhooks:   []WebhookTarget{},
	}
	if _, _, err := mgr.Update(cleared); err != nil {
		t.Fatalf("clear update failed: %v", err)
	}

	got := mgr.Get()
	wc := got.Notifiers["wechat"]
	if wc.WebhookURL != "" || wc.Webhooks == nil || len(wc.Webhooks) != 0 {
		t.Fatalf("expected cleared webhooks to stay empty, got %+v", wc)
	}
	if got.HasConfiguredClient() {
		t.Fatal("expected HasConfiguredClient false after clearing all webhooks")
	}

	// Reload from disk and verify old URL was not resurrected
	if _, err := mgr.Reload(); err != nil {
		t.Fatalf("Reload failed: %v", err)
	}
	reloaded := mgr.Get().Notifiers["wechat"]
	if reloaded.WebhookURL != "" || len(reloaded.Webhooks) != 0 {
		t.Fatalf("expected cleared webhooks after Reload, got %+v", reloaded)
	}
}

func TestMultiURLEnvOverrides(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	t.Setenv("ALERT_WEBHOOK_WECHAT_URL", "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=w1, https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=w2")
	t.Setenv("ALERT_WEBHOOK_WECHAT_URLS", "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=w2\nhttps://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=w3")
	t.Setenv("ALERT_WEBHOOK_DINGTALK_URLS", "https://oapi.dingtalk.com/robot/send?access_token=d1,https://oapi.dingtalk.com/robot/send?access_token=d2")
	t.Setenv("ALERT_WEBHOOK_FEISHU_URL", "https://open.feishu.cn/open-apis/bot/v2/hook/f1")

	mgr, err := NewConfigManager(cfgPath)
	if err != nil {
		t.Fatalf("NewConfigManager failed: %v", err)
	}

	cfg := mgr.Get()
	if len(cfg.Clients) != 3 {
		t.Fatalf("expected 3 auto-enabled clients, got %v", cfg.Clients)
	}

	wcTargets := cfg.Notifiers["wechat"].AllTargets()
	if len(wcTargets) != 3 {
		t.Fatalf("expected 3 deduplicated wechat targets, got %+v", wcTargets)
	}
	if wcTargets[0].URL != "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=w1" ||
		wcTargets[1].URL != "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=w2" ||
		wcTargets[2].URL != "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=w3" {
		t.Fatalf("unexpected wechat target order/urls: %+v", wcTargets)
	}
	if cfg.Notifiers["wechat"].PrimaryURL() != "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=w1" {
		t.Fatalf("unexpected primary URL: %q", cfg.Notifiers["wechat"].PrimaryURL())
	}

	dtTargets := cfg.Notifiers["dingtalk"].AllTargets()
	if len(dtTargets) != 2 {
		t.Fatalf("expected 2 dingtalk targets from ALERT_WEBHOOK_DINGTALK_URLS, got %+v", dtTargets)
	}
	fsTargets := cfg.Notifiers["feishu"].AllTargets()
	if len(fsTargets) != 1 {
		t.Fatalf("expected 1 feishu target, got %+v", fsTargets)
	}
}

func TestValidateConfigMultiWebhookErrorsAndWarnings(t *testing.T) {
	cfg := DefaultAppConfig()
	cfg.Clients = []string{"wechat", "dingtalk", "feishu"}
	cfg.Notifiers["wechat"] = NotifierConfig{
		Webhooks: []WebhookTarget{
			{Name: "群1", URL: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=real1"},
			{Name: "群2", URL: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=real1"}, // duplicate
			{Name: "坏URL", URL: "ftp://invalid-scheme.example.com/hook"},                // invalid format
		},
	}
	cfg.Notifiers["dingtalk"] = NotifierConfig{
		Webhooks: []WebhookTarget{
			{Name: "已禁用", URL: "https://oapi.dingtalk.com/robot/send?access_token=real", Enabled: boolPtr(false)},
		},
	}
	cfg.Notifiers["feishu"] = NotifierConfig{
		Webhooks: []WebhookTarget{
			{URL: "https://open.feishu.cn/open-apis/bot/v2/hook/xxxxxx"}, // placeholder
		},
	}

	issues := ValidateConfig(cfg)
	if !HasValidationErrors(issues) {
		t.Fatalf("expected validation errors, got %+v", issues)
	}

	findIssue := func(field, level, substr string) bool {
		for _, iss := range issues {
			if iss.Field == field && iss.Level == level && strings.Contains(iss.Message, substr) {
				return true
			}
		}
		return false
	}

	if !findIssue("notifiers.wechat.webhooks[1].url", "warning", "重复") {
		t.Fatalf("expected duplicate URL warning on wechat webhooks[1], got %+v", issues)
	}
	if !findIssue("notifiers.wechat.webhooks[2].url", "error", "格式无效") {
		t.Fatalf("expected invalid URL error on wechat webhooks[2], got %+v", issues)
	}
	if !findIssue("notifiers.dingtalk.webhooks", "warning", "已启用 dingtalk 但没有已启用且有效的 Webhook URL") {
		t.Fatalf("expected no-enabled-valid-url warning on dingtalk, got %+v", issues)
	}
	if !findIssue("notifiers.feishu.webhooks[0].url", "warning", "占位符") {
		t.Fatalf("expected placeholder warning on feishu webhooks[0], got %+v", issues)
	}
	if !findIssue("notifiers.feishu.webhooks", "warning", "已启用 feishu 但没有已启用且有效的 Webhook URL") {
		t.Fatalf("expected no-enabled-valid-url warning on feishu, got %+v", issues)
	}

	// Unenabled client with invalid URL must still produce error on webhooks[i].url
	cfgUnenabled := DefaultAppConfig()
	cfgUnenabled.Clients = []string{}
	cfgUnenabled.Notifiers["dingtalk"] = NotifierConfig{
		Webhooks: []WebhookTarget{
			{URL: "not-a-valid-url"},
		},
	}
	unenabledIssues := ValidateConfig(cfgUnenabled)
	foundUnenabledErr := false
	for _, iss := range unenabledIssues {
		if iss.Field == "notifiers.dingtalk.webhooks[0].url" && iss.Level == "error" {
			foundUnenabledErr = true
		}
	}
	if !foundUnenabledErr {
		t.Fatalf("expected error on unenabled dingtalk webhooks[0].url, got %+v", unenabledIssues)
	}
}

func TestTestClientsConnectionMultiWebhook(t *testing.T) {
	var mu sync.Mutex
	hits := map[string]int{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		mu.Unlock()
		if r.URL.Path == "/fail" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"errcode": 40001, "errmsg": "bad key"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"errcode": 0, "errmsg": "ok"}`))
	}))
	defer srv.Close()

	cfg := DefaultAppConfig()
	cfg.Clients = []string{"wechat"}
	cfg.Notifiers["wechat"] = NotifierConfig{
		Webhooks: []WebhookTarget{
			{Name: "运维群", URL: srv.URL + "/ok1"},
			{URL: srv.URL + "/ok2"},
			{Name: "已禁用", URL: srv.URL + "/disabled", Enabled: boolPtr(false)},
			{Name: "占位符", URL: srv.URL + "/xxxxxx"},
		},
	}
	cfg.ApplyDefaults()
	SyncGlobals(cfg)

	var logBuf bytes.Buffer
	origWriter := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(origWriter)

	if ok := TestClientsConnection(); !ok {
		t.Fatalf("expected TestClientsConnection true, log:\n%s", logBuf.String())
	}

	mu.Lock()
	if hits["/ok1"] != 1 || hits["/ok2"] != 1 || hits["/disabled"] != 0 || hits["/xxxxxx"] != 0 {
		mu.Unlock()
		t.Fatalf("unexpected hit counts: %+v", hits)
	}
	mu.Unlock()

	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "wechat(运维群)") || !strings.Contains(logOutput, "wechat(#2)") {
		t.Fatalf("expected formatted target labels in logs, got:\n%s", logOutput)
	}

	// Failure on one target returns false while still testing other enabled targets
	cfgFail := cfg.Clone()
	cfgFail.Notifiers["wechat"] = NotifierConfig{
		Webhooks: []WebhookTarget{
			{Name: "主群", URL: srv.URL + "/ok1"},
			{Name: "故障群", URL: srv.URL + "/fail"},
		},
	}
	SyncGlobals(cfgFail)

	logBuf.Reset()
	if ok := TestClientsConnection(); ok {
		t.Fatal("expected TestClientsConnection false when one target fails")
	}
	if !strings.Contains(logBuf.String(), "wechat(故障群)") {
		t.Fatalf("expected failure log with target label, got:\n%s", logBuf.String())
	}

	// Network/dial failure must not leak secret query tokens in logs
	closedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	closedURL := closedSrv.URL + "/cgi-bin/webhook/send?key=super-secret-leak-check"
	closedSrv.Close()

	cfgDialErr := DefaultAppConfig()
	cfgDialErr.Clients = []string{"wechat"}
	cfgDialErr.Notifiers["wechat"] = NotifierConfig{
		Webhooks: []WebhookTarget{
			{Name: "不可达群", URL: closedURL},
		},
	}
	cfgDialErr.ApplyDefaults()
	SyncGlobals(cfgDialErr)

	logBuf.Reset()
	if ok := TestClientsConnection(); ok {
		t.Fatal("expected TestClientsConnection false on dial error")
	}
	if strings.Contains(logBuf.String(), "super-secret-leak-check") {
		t.Fatalf("expected secret token redacted from error logs, got:\n%s", logBuf.String())
	}
}

func TestLoadDotEnvAndAPIKeyPrecedence(t *testing.T) {
	dir := t.TempDir()

	// Helper to ensure test env keys do not leak across tests.
	setAndRestore := func(keys ...string) {
		for _, k := range keys {
			orig, had := os.LookupEnv(k)
			key := k
			t.Cleanup(func() {
				if had {
					_ = os.Setenv(key, orig)
				} else {
					_ = os.Unsetenv(key)
				}
			})
			_ = os.Unsetenv(key)
		}
	}

	setAndRestore(
		"DOTENV_PLAIN",
		"DOTENV_EXPORT",
		"DOTENV_SINGLE",
		"DOTENV_DOUBLE",
		"DOTENV_INLINE",
		"DOTENV_OS_WIN",
		"API_KEY",
		"ALERT_WEBHOOK_API_KEY",
		"ALERT_WEBHOOK_AUTH_TOKEN",
		"ALERT_WEBHOOK_ENV_FILE",
	)

	// Missing explicit file returns empty path without error.
	if loaded, err := LoadDotEnv(filepath.Join(dir, "missing.env")); err != nil || loaded != "" {
		t.Fatalf("expected missing .env to be non-fatal, got loaded=%q err=%v", loaded, err)
	}

	envPath := filepath.Join(dir, ".env")
	envContent := "\ufeff# comment line\n\n" +
		"DOTENV_PLAIN=hello=world\n" +
		"export DOTENV_EXPORT=exported_val\n" +
		"DOTENV_SINGLE='single # literal'\n" +
		"DOTENV_DOUBLE=\"line1\\nline2\\\"quoted\\\"\"\n" +
		"DOTENV_INLINE=trimmed_val # inline comment\n" +
		"DOTENV_OS_WIN=from_dotenv\n" +
		"API_KEY=dotenv-api-key\n"
	if err := os.WriteFile(envPath, []byte(envContent), 0644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("DOTENV_OS_WIN", "from-os-env")
	t.Setenv("ALERT_WEBHOOK_ENV_FILE", envPath)

	loaded, err := LoadDotEnv("")
	if err != nil || loaded != envPath {
		t.Fatalf("expected LoadDotEnv to load %s, got %q err=%v", envPath, loaded, err)
	}

	if got := os.Getenv("DOTENV_PLAIN"); got != "hello=world" {
		t.Fatalf("unexpected DOTENV_PLAIN: %q", got)
	}
	if got := os.Getenv("DOTENV_EXPORT"); got != "exported_val" {
		t.Fatalf("unexpected DOTENV_EXPORT: %q", got)
	}
	if got := os.Getenv("DOTENV_SINGLE"); got != "single # literal" {
		t.Fatalf("unexpected DOTENV_SINGLE: %q", got)
	}
	if got := os.Getenv("DOTENV_DOUBLE"); got != "line1\nline2\"quoted\"" {
		t.Fatalf("unexpected DOTENV_DOUBLE: %q", got)
	}
	if got := os.Getenv("DOTENV_INLINE"); got != "trimmed_val" {
		t.Fatalf("unexpected DOTENV_INLINE: %q", got)
	}
	if got := os.Getenv("DOTENV_OS_WIN"); got != "from-os-env" {
		t.Fatalf("expected OS env to take precedence over .env, got %q", got)
	}

	// API_KEY precedence: API_KEY > ALERT_WEBHOOK_API_KEY > ALERT_WEBHOOK_AUTH_TOKEN
	cfgPath := filepath.Join(dir, "config.yaml")
	t.Setenv("ALERT_WEBHOOK_AUTH_TOKEN", "legacy-token")
	t.Setenv("ALERT_WEBHOOK_API_KEY", "prefixed-key")

	mgr1, err := NewConfigManager(cfgPath)
	if err != nil {
		t.Fatalf("NewConfigManager failed: %v", err)
	}
	if got := mgr1.Get().Server.AuthToken; got != "dotenv-api-key" {
		t.Fatalf("expected API_KEY to win over prefixed/legacy tokens, got %q", got)
	}

	_ = os.Unsetenv("API_KEY")
	mgr2, err := NewConfigManager(cfgPath)
	if err != nil {
		t.Fatalf("NewConfigManager failed: %v", err)
	}
	if got := mgr2.Get().Server.AuthToken; got != "prefixed-key" {
		t.Fatalf("expected ALERT_WEBHOOK_API_KEY to win over ALERT_WEBHOOK_AUTH_TOKEN, got %q", got)
	}
}
