package config

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
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
