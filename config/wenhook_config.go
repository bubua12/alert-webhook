package config

import (
	"fmt"
	"gopkg.in/yaml.v2"
	"io/ioutil"
)

type WeChatConfig struct {
	WebhookURL string `yaml:"webhook_url"`
}

type ServerConfig struct {
	Port string `yaml:"port"`
}

type AppConfig struct {
	WeChat WeChatConfig `yaml:"wechat"`
	Server ServerConfig `yaml:"server"`
}

func LoadConfig(path string) (*AppConfig, error) {
	config := &AppConfig{}

	file, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}

	err = yaml.Unmarshal(file, config)
	if err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}

	// 验证企微配置
	if config.WeChat.WebhookURL == "" {
		return nil, fmt.Errorf("企微Webhook URL未配置")
	}

	return config, nil
}
