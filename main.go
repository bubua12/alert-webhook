package main

import (
	"alert-webhook/config"
	"alert-webhook/console"
	"alert-webhook/service"
	"alert-webhook/store"
	"alert-webhook/web"
	"flag"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	_ "time/tzdata"

	"github.com/natefinch/lumberjack"
)

func main() {
	loadedEnv, envErr := config.LoadDotEnv("")

	defaultConfigPath := strings.TrimSpace(os.Getenv("ALERT_WEBHOOK_CONFIG"))
	if defaultConfigPath == "" {
		defaultConfigPath = "./config.yaml"
	}
	configPath := flag.String("config", defaultConfigPath, "配置文件路径")
	flag.Parse()

	logBuffer := store.NewLogBuffer(500)
	// Configure initial stdout + memory log buffer before loading config so startup logs are captured.
	log.SetOutput(io.MultiWriter(os.Stdout, logBuffer))
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	if envErr != nil {
		log.Printf("[Warn] 加载环境变量文件失败: %v", envErr)
	} else if loadedEnv != "" {
		log.Printf("已加载环境变量文件: %s", loadedEnv)
	}

	cfgMgr, err := config.NewConfigManager(*configPath)
	if err != nil {
		log.Fatalf("配置初始化失败: %v", err)
	}
	console.Success("[Success]", "配置初始化成功: "+cfgMgr.ConfigPath())

	cfg := cfgMgr.Get()
	setupLogging(cfg.Server.LogDir, logBuffer)

	service.RegisterUIFS(web.StaticFS())

	alertStore := store.NewAlertStore(cfg.Server.DataDir, 1000)
	app := service.NewAppLauncher(cfgMgr, alertStore, logBuffer)
	app.Run()
}

func setupLogging(logDir string, logBuffer *store.LogBuffer) {
	dir := strings.TrimSpace(logDir)
	if dir == "" {
		dir = "./logs"
	}

	writers := []io.Writer{os.Stdout}
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Printf("[Warn] 无法创建日志目录 %s: %v，将仅输出到标准输出与内存缓冲", dir, err)
	} else {
		lumberjackLogger := &lumberjack.Logger{
			Filename:   filepath.Join(dir, "alter-server.log"),
			MaxSize:    1,
			MaxBackups: 5,
			MaxAge:     7,
			Compress:   true,
		}
		writers = append(writers, lumberjackLogger)
	}
	if logBuffer != nil {
		writers = append(writers, logBuffer)
	}

	log.SetOutput(io.MultiWriter(writers...))
	log.SetFlags(log.LstdFlags | log.Lshortfile)
}
