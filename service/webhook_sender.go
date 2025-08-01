package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
)

type MarkdownMessage struct {
	Content string `json:"content"`
}

// WeChatMessage 消息结构体定义 ----------- https://developer.work.weixin.qq.com/document/path/91770
/**
{
  "msgtype": "markdown",
  "markdown": {
    "content": "你的告警内容"
  }
}
*/
type WeChatMessage struct {
	MsgType  string          `json:"msgtype"`  // 消息类型：例如 "markdown"
	Markdown MarkdownMessage `json:"markdown"` // markdown 消息体
}

// WecomSendAlert 向企业微信的 Webhook URL 发送 markdown 格式的消息
func WecomSendAlert(webhookURL string, message string) error {
	// 构造消息体，符合企业微信的 markdown 消息格式要求
	msg := WeChatMessage{
		MsgType: "markdown",
		Markdown: MarkdownMessage{
			Content: message,
		},
	}

	// 将结构体编码为 JSON 数据，企业微信要求的请求体必须是 JSON
	jsonData, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("JSON编码失败: %w", err)
	}

	// 发送 HTTP POST 请求到企业微信的 webhook 接口、Content-Type 设置为 application/json
	resp, err := http.Post(webhookURL, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("HTTP请求失败: %w", err)
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			log.Printf("关闭响应体失败: %v", err)
		}
	}(resp.Body)

	// 检查 HTTP 状态码是否为 200、如果不是 200，说明企业微信返回了错误
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("企微返回错误状态码: %d", resp.StatusCode)
	}

	return nil
}

// DingdingMessage 钉钉消息结构体 ----------- https://open.dingtalk.com/document/robots/custom-robot-access
/**
{
  "msgtype": "markdown",
  "markdown": {
    "title": "告警通知",
    "text": "你的告警内容"
  }
}
*/
type DingdingMessage struct {
	MsgType  string `json:"msgtype"` // 消息类型：固定为 "markdown"
	Markdown struct {
		Title string `json:"title"` // 消息标题
		Text  string `json:"text"`  // 消息内容
	} `json:"markdown"`
}

// DingdingSendAlert 向钉钉发送告警
func DingdingSendAlert(webhookURL string, message string) error {
	msg := DingdingMessage{
		MsgType: "markdown",
	}
	msg.Markdown.Title = "告警通知"
	msg.Markdown.Text = message

	jsonData, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("JSON编码失败: %w", err)
	}

	resp, err := http.Post(webhookURL, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("HTTP请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("钉钉返回错误: 状态码=%d, 响应=%s", resp.StatusCode, string(body))
	}

	return nil
}

// FeishuMessage 飞书消息结构体 ----------- https://open.feishu.cn/document/client-docs/bot-v3/add-custom-bot
/**
{
  "msg_type": "interactive",
  "card": {
    "elements": [
      {
        "tag": "markdown",
        "content": "你的告警内容"
      }
    ],
    "header": {
      "title": {
        "content": "告警通知",
        "tag": "plain_text"
      }
    }
  }
}
*/
type FeishuMessage struct {
	MsgType string `json:"msg_type"` // 消息类型：固定为 "interactive"
	Card    struct {
		Elements []struct {
			Tag     string `json:"tag"`     // 固定为 "markdown"
			Content string `json:"content"` // markdown内容
		} `json:"elements"`
		Header struct {
			Title struct {
				Content string `json:"content"` // 标题内容
				Tag     string `json:"tag"`     // 固定为 "plain_text"
			} `json:"title"`
		} `json:"header"`
	} `json:"card"`
}

// FeishuSendAlert 向飞书发送告警
func FeishuSendAlert(webhookURL string, message string) error {
	msg := FeishuMessage{
		MsgType: "interactive",
	}
	msg.Card.Elements = []struct {
		Tag     string `json:"tag"`
		Content string `json:"content"`
	}{
		{
			Tag:     "markdown",
			Content: message,
		},
	}
	msg.Card.Header.Title.Content = "告警通知"
	msg.Card.Header.Title.Tag = "plain_text"

	jsonData, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("JSON编码失败: %w", err)
	}

	resp, err := http.Post(webhookURL, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("HTTP请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("飞书返回错误: 状态码=%d, 响应=%s", resp.StatusCode, string(body))
	}

	return nil
}
