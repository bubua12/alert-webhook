package config

import (
	"fmt"
	"gopkg.in/yaml.v2"
	"os"
)

type NotifierConfig struct {
	WebhookURL string `yaml:"webhook_url"`
}

type ServerConfig struct {
	Port string `yaml:"port"`
}

type AppConfig struct {
	Clients   []string                  `yaml:"client"`
	Notifiers map[string]NotifierConfig `yaml:"notifiers"`
	Server    ServerConfig              `yaml:"server"`
}

// LoadConfig 根据传入配置文件的路径 --- 加载配置
func LoadConfig(path string) (*AppConfig, error) {
	config := &AppConfig{}

	file, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}

	err = yaml.Unmarshal(file, config)
	if err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}

	// 验证配置
	if len(config.Clients) == 0 {
		return nil, fmt.Errorf("未配置任何客户端")
	}

	for _, client := range config.Clients {
		if _, ok := config.Notifiers[client]; !ok {
			return nil, fmt.Errorf("客户端 %s 的配置缺失", client)
		}
		if config.Notifiers[client].WebhookURL == "" {
			return nil, fmt.Errorf("客户端 %s 的Webhook URL未配置", client)
		}
	}

	return config, nil
}
