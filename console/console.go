package console

import "fmt"

const (
	ColorRed    = "\033[31m"
	ColorGreen  = "\033[32m"
	ColorYellow = "\033[33m"
	ColorBlue   = "\033[34m"
	ColorReset  = "\033[0m"
)

// Success 绿色前缀
func Success(prefix, msg string) {
	fmt.Printf("%s%s%s %s\n", ColorGreen, prefix, ColorReset, msg)
}

// Error 红色前缀
func Error(prefix, msg string) {
	fmt.Printf("%s%s%s %s\n", ColorRed, prefix, ColorReset, msg)
}

// Warning 黄色前缀
func Warning(prefix, msg string) {
	fmt.Printf("%s%s%s %s\n", ColorYellow, prefix, ColorReset, msg)
}

// Info 蓝色前缀
func Info(prefix, msg string) {
	fmt.Printf("%s%s%s %s\n", ColorBlue, prefix, ColorReset, msg)
}

// Plain 普通打印（无前缀）
func Plain(msg string) {
	fmt.Println(msg)
}
