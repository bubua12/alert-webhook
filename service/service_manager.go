package service

import (
	"alert-webhook/config"
	"alert-webhook/console"
	"log"
)

// ServiceManager 服务管理器
type ServiceManager struct {
	clickhouseService   *ClickHouseService
	trafficAlertService *TrafficAlertService
}

// NewServiceManager 创建服务管理器
func NewServiceManager() *ServiceManager {
	return &ServiceManager{}
}

// InitializeTrafficAlert 初始化大流量告警服务
func (sm *ServiceManager) InitializeTrafficAlert() {
	if !config.GlobalConfig.TrafficAlert.Enabled {
		log.Println("大流量告警功能未配置启用，将不开启流量监控功能")
		console.Warning("[Warning]", "大流量告警功能未启用，将不开启流量监控功能")
		return
	}

	// 初始化ClickHouse服务
	var err error
	sm.clickhouseService, err = NewClickHouseService(config.GlobalConfig)
	if err != nil {
		log.Printf("ClickHouse服务初始化失败: %v", err)
		console.Error("[Error]", "ClickHouse连接失败，大流量告警功能将被禁用")
		return
	}

	// 测试ClickHouse连接
	if err := sm.clickhouseService.TestConnection(); err != nil {
		log.Printf("ClickHouse连接测试失败: %v", err)
		console.Error("[Error]", "ClickHouse连接测试失败，大流量告警功能将被禁用")
		err := sm.clickhouseService.Close()
		if err != nil {
			return
		}
		sm.clickhouseService = nil
		return
	}

	// 启动流量告警服务
	sm.trafficAlertService = NewTrafficAlertService(
		sm.clickhouseService,
		config.GlobalConfig,
		config.Notifiers,
		config.EnabledClients,
	)
	sm.trafficAlertService.Start()
	console.Success("[Success]", "大流量告警服务启动成功")
}

// Shutdown 优雅关闭所有服务
func (sm *ServiceManager) Shutdown() {
	log.Println("正在关闭服务管理器...")

	// 停止流量告警服务
	if sm.trafficAlertService != nil {
		sm.trafficAlertService.Stop()
		log.Println("大流量告警服务已停止")
	}

	// 关闭ClickHouse连接
	if sm.clickhouseService != nil {
		if err := sm.clickhouseService.Close(); err != nil {
			log.Printf("关闭ClickHouse连接失败: %v", err)
		} else {
			log.Println("ClickHouse连接已关闭")
		}
	}
}

// IsTrafficAlertEnabled 检查大流量告警是否已启用
func (sm *ServiceManager) IsTrafficAlertEnabled() bool {
	return sm.trafficAlertService != nil && sm.clickhouseService != nil
}
