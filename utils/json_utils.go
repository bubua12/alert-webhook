package utils

import (
	"encoding/json"
	"fmt"
)

// PrintJSON 将任意结构体以 JSON 格式美观地打印到控制台。
// 参数：v 需要打印的结构体（interface{} 可以接收任何类型）
// 当序列化失败时，会打印错误信息。
func PrintJSON(v interface{}) {
	jsonBytes, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Printf("序列化失败: %v\n", err)
		return
	}
	fmt.Println(string(jsonBytes))
}

// FormatJSON 将结构体序列化为格式化（缩进）的 JSON 字符串。
func FormatJSON(v interface{}) (string, error) {
	jsonBytes, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(jsonBytes), nil
}

// CompactJSON 将结构体序列化为紧凑的 JSON 字符串（不带换行和缩进）。
func CompactJSON(v interface{}) (string, error) {
	jsonBytes, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(jsonBytes), nil
}

// IsValidJSON 判断一个字符串是否是合法 JSON 格式。
func IsValidJSON(data string) bool {
	var js json.RawMessage
	return json.Unmarshal([]byte(data), &js) == nil
}

// ParseJSON 将 JSON 字符串反序列化为指定的对象。
// 参数 target 必须是一个指针类型。
func ParseJSON(data string, target interface{}) error {
	return json.Unmarshal([]byte(data), target)
}
