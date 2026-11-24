package service

import (
	"alert-webhook/config"
	"alert-webhook/console"
	"alert-webhook/store"
	"log"
	"os"
	"os/signal"
	"syscall"
)

// AppLauncher 应用启动器
type AppLauncher struct {
	cfgMgr         *config.ConfigManager
	alertStore     *store.AlertStore
	logBuffer      *store.LogBuffer
	serviceManager *ServiceManager
	serverManager  *ServerManager
}

// NewAppLauncher 创建应用启动器
func NewAppLauncher(cfgMgr *config.ConfigManager, alertStore *store.AlertStore, logBuffer *store.LogBuffer) *AppLauncher {
	svcMgr := NewServiceManager(cfgMgr, alertStore)
	srvMgr := NewServerManager(cfgMgr, alertStore, logBuffer, svcMgr)
	return &AppLauncher{
		cfgMgr:         cfgMgr,
		alertStore:     alertStore,
		logBuffer:      logBuffer,
		serviceManager: svcMgr,
		serverManager:  srvMgr,
	}
}

// Run 启动应用程序并阻塞直到收到关闭信号或服务异常退出
func (app *AppLauncher) Run() {
	cfg := app.cfgMgr.Get()

	// 1. 注册配置热更新回调
	app.cfgMgr.OnReload(func(newCfg *config.AppConfig) {
		log.Println("检测到配置更新，正在热重载后台监控服务...")
		app.serviceManager.Reload(newCfg)
	})

	// 2. 初始化后台监控服务
	app.serviceManager.InitializeAll(cfg)

	// 3. 若配置了启用客户端，异步测试连通性，避免阻塞服务启动
	if len(cfg.Clients) == 0 {
		log.Println("[Warn] 当前未启用任何通知渠道，请通过 Web 控制台或环境变量配置")
		console.Warning("[Warning]", "当前未启用任何通知渠道，已进入控制台引导模式")
	} else {
		go app.testClientsConnection()
	}

	// 4. 监听优雅关闭信号
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigChan)

	errChan := make(chan error, 1)
	console.Success("[Running]", "服务已启动，端口信息: "+cfg.Server.Port)
	go func() {
		errChan <- app.serverManager.StartWebhookServer(cfg.Server.Port)
	}()

	select {
	case err := <-errChan:
		if err != nil {
			log.Printf("Webhook 服务启动失败: %v", err)
			app.serviceManager.Shutdown()
			os.Exit(1)
		}
	case sig := <-sigChan:
		log.Printf("收到关闭信号 (%s)，正在优雅关闭...", sig)
		if err := app.serverManager.Shutdown(); err != nil {
			log.Printf("关闭HTTP服务器失败: %v", err)
		}
		app.serviceManager.Shutdown()
		if err := <-errChan; err != nil {
			log.Printf("HTTP服务器退出异常: %v", err)
		}
		log.Println("应用程序已优雅关闭")
	}
}

// testClientsConnection 测试客户端连通性
func (app *AppLauncher) testClientsConnection() {
	allSuccess := config.TestClientsConnection()
	if allSuccess {
		console.Success("[Success]", "所有客户端连通性测试成功")
	} else {
		console.Error("[Failed]", "部分客户端连通性测试失败，将继续启动服务")
	}
}
