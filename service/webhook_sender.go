package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
)

// WeChatMessage 企业微信消息结构
type WeChatMessage struct {
	MsgType  string          `json:"msgtype"`
	Markdown MarkdownMessage `json:"markdown"`
}

type MarkdownMessage struct {
	Content string `json:"content"`
}

// 钉钉消息结构
type DingTalkMessage struct {
	MsgType  string           `json:"msgtype"`
	Markdown DingTalkMarkdown `json:"markdown"`
}

type DingTalkMarkdown struct {
	Title string `json:"title"`
	Text  string `json:"text"`
}

// 飞书消息结构
type FeishuMessage struct {
	MsgType string        `json:"msg_type"`
	Content FeishuContent `json:"content"`
}

type FeishuContent struct {
	Text string `json:"text"`
}

// 第三方API消息结构
type ThirdPartyAPIMessage struct {
	Inputs       ThirdPartyInputs `json:"inputs"`
	ResponseMode string           `json:"response_mode"`
	User         string           `json:"user"`
}

type ThirdPartyInputs struct {
	Query string `json:"query"`
}

// SendAlert 发送告警到指定客户端
func SendAlert(client, webhookURL string, message interface{}) error {
	jsonData, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("[%s] JSON编码失败: %w", client, err)
	}

	fmt.Println("JSON消息格式: \n", string(jsonData))

	resp, err := http.Post(webhookURL, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("[%s] HTTP请求失败: %w", client, err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("[%s] 关闭响应体失败: %v", client, err)
		}
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("[%s] 返回错误状态码: %d, 响应: %s",
			client, resp.StatusCode, string(body))
	}

	return nil
}

// SendThirdPartyAPI 发送告警到第三方API
func SendThirdPartyAPI(apiName, apiURL, apiKey, alertContent string) error {
	// 构建请求数据
	message := ThirdPartyAPIMessage{
		Inputs: ThirdPartyInputs{
			Query: alertContent,
		},
		ResponseMode: "streaming",
		User:         "abc-123",
	}

	jsonData, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("[%s] JSON编码失败: %w", apiName, err)
	}

	// 创建HTTP请求
	req, err := http.NewRequest("POST", apiURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("[%s] 创建请求失败: %w", apiName, err)
	}

	// 设置请求头
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	fmt.Printf("[第三方API %s] JSON消息格式: \n%s\n", apiName, string(jsonData))

	// 发送请求
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("[%s] HTTP请求失败: %w", apiName, err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("[%s] 关闭响应体失败: %v", apiName, err)
		}
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("[%s] 返回错误状态码: %d, 响应: %s",
			apiName, resp.StatusCode, string(body))
	}

	log.Printf("[第三方API %s] 发送成功", apiName)
	return nil
}
