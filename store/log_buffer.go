package store

import (
	"alert-webhook/utils"
	"bytes"
	"regexp"
	"strings"
	"sync"
	"time"
)

const defaultLogBufferCapacity = 500

var ansiEscapeRegex = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// LogEntry represents a single captured log line.
type LogEntry struct {
	ID        int64  `json:"id"`
	Timestamp string `json:"timestamp"`
	Level     string `json:"level"` // "INFO" | "WARN" | "ERROR"
	Message   string `json:"message"`
}

// LogBuffer is a thread-safe ring buffer io.Writer capturing recent application logs.
type LogBuffer struct {
	mu       sync.RWMutex
	capacity int
	entries  []LogEntry
	nextID   int64
	pending  bytes.Buffer
}

// NewLogBuffer creates a LogBuffer retaining up to capacity log lines (defaults to 500).
func NewLogBuffer(capacity int) *LogBuffer {
	if capacity <= 0 {
		capacity = defaultLogBufferCapacity
	}
	return &LogBuffer{
		capacity: capacity,
		entries:  make([]LogEntry, 0, min(capacity, 64)),
		nextID:   1,
	}
}

// Write implements io.Writer, buffering partial writes and recording complete lines.
func (b *LogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.pending.Write(p)
	for {
		line, err := b.pending.ReadString('\n')
		if err != nil {
			// Incomplete line without trailing newline; put back in pending buffer.
			b.pending.WriteString(line)
			break
		}
		b.appendLineLocked(line)
	}
	return len(p), nil
}

func (b *LogBuffer) appendLineLocked(rawLine string) {
	cleaned := ansiEscapeRegex.ReplaceAllString(rawLine, "")
	cleaned = strings.TrimRight(cleaned, "\r\n")
	if strings.TrimSpace(cleaned) == "" {
		return
	}

	loc := utils.GetShanghaiLocation()
	ts := time.Now().In(loc)

	// Extract Go standard logger date/time prefix (e.g. "2026/11/24 11:30:00 ") if present.
	if len(cleaned) >= 19 && cleaned[4] == '/' && cleaned[7] == '/' && cleaned[10] == ' ' {
		if parsed, err := time.ParseInLocation("2006/01/02 15:04:05", cleaned[:19], loc); err == nil {
			ts = parsed
		}
	}

	entry := LogEntry{
		ID:        b.nextID,
		Timestamp: ts.Format(time.RFC3339),
		Level:     classifyLogLevel(cleaned),
		Message:   cleaned,
	}
	b.nextID++

	if len(b.entries) >= b.capacity {
		copy(b.entries, b.entries[1:])
		b.entries[len(b.entries)-1] = entry
	} else {
		b.entries = append(b.entries, entry)
	}
}

func classifyLogLevel(msg string) string {
	lower := strings.ToLower(msg)
	if strings.Contains(lower, "[error]") ||
		strings.Contains(lower, "[failed]") ||
		strings.Contains(lower, "error") ||
		strings.Contains(lower, "failed") ||
		strings.Contains(lower, "fatal") ||
		strings.Contains(lower, "panic") ||
		strings.Contains(msg, "失败") ||
		strings.Contains(msg, "异常") ||
		strings.Contains(msg, "错误") {
		return "ERROR"
	}
	if strings.Contains(lower, "[warn]") ||
		strings.Contains(lower, "[warning]") ||
		strings.Contains(lower, "warn") ||
		strings.Contains(msg, "[警告]") ||
		strings.Contains(msg, "警告") ||
		strings.Contains(msg, "忽略") {
		return "WARN"
	}
	return "INFO"
}

// Query filters captured logs by level and keyword, returning up to limit latest entries and total matches.
func (b *LogBuffer) Query(level, q string, limit int) ([]LogEntry, int) {
	if limit <= 0 {
		limit = 200
	}
	if limit > b.capacity {
		limit = b.capacity
	}

	levelFilter := strings.ToUpper(strings.TrimSpace(level))
	if levelFilter == "ALL" {
		levelFilter = ""
	}
	keyword := strings.ToLower(strings.TrimSpace(q))

	b.mu.RLock()
	matched := make([]LogEntry, 0, len(b.entries))
	for _, entry := range b.entries {
		if levelFilter != "" && entry.Level != levelFilter {
			continue
		}
		if keyword != "" &&
			!strings.Contains(strings.ToLower(entry.Message), keyword) &&
			!strings.Contains(strings.ToLower(entry.Level), keyword) {
			continue
		}
		matched = append(matched, entry)
	}
	b.mu.RUnlock()

	total := len(matched)
	if total > limit {
		matched = matched[total-limit:]
	}
	if matched == nil {
		matched = []LogEntry{}
	}
	return matched, total
}
