package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
)

// 企业微信消息结构
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

// SendAlert 发送告警到指定客户端
func SendAlert(client, webhookURL string, message interface{}) error {
	jsonData, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("[%s] JSON编码失败: %w", client, err)
	}

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
