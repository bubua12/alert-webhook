package service

import (
	"alert-webhook/config"
	"alert-webhook/console"
	"log"
	"os"
	"os/signal"
	"syscall"
)

// AppLauncher 应用启动器
type AppLauncher struct {
	serviceManager *ServiceManager
	serverManager  *ServerManager
}

// NewAppLauncher 创建应用启动器
func NewAppLauncher() *AppLauncher {
	return &AppLauncher{
		serviceManager: NewServiceManager(),
		serverManager:  NewServerManager(),
	}
}

// Run 启动应用程序
func (app *AppLauncher) Run() {
	// 1. 测试客户端连通性
	app.testClientsConnection()

	// 2. 初始化服务
	app.initializeServices()

	// 3. 设置优雅关闭
	app.setupGracefulShutdown()

	// 4. 启动服务器
	console.Success("[Running]", "服务已启动，端口信息: "+config.ServerPort)
	if err := app.serverManager.StartWebhookServer(config.ServerPort); err != nil {
		log.Fatalf("Webhook 服务启动失败: %v", err)
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

// initializeServices 初始化所有服务
func (app *AppLauncher) initializeServices() {
	// 初始化大流量告警服务
	app.serviceManager.InitializeTrafficAlert()
}

// setupGracefulShutdown 设置优雅关闭
func (app *AppLauncher) setupGracefulShutdown() {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Println("收到关闭信号，正在优雅关闭...")

		// 关闭所有服务
		app.serviceManager.Shutdown()

		// 关闭HTTP服务器
		if err := app.serverManager.Shutdown(); err != nil {
			log.Printf("关闭HTTP服务器失败: %v", err)
		}

		log.Println("应用程序已优雅关闭")
		os.Exit(0)
	}()
}
