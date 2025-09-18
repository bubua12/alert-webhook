package main

import (
	"alert-webhook/service"
	"log"

	"github.com/natefinch/lumberjack"
)

func main() {
	// 创建并启动应用
	app := service.NewAppLauncher()
	app.Run()
}

// init 设置日志轮转配置
func init() {
	log.SetOutput(&lumberjack.Logger{
		Filename:   "./logs/alter-server.log",
		MaxSize:    1,
		MaxBackups: 5,
		MaxAge:     7,
		Compress:   true,
	})
	log.SetFlags(log.LstdFlags | log.Lshortfile)
}
