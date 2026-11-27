package store

import (
	"alert-webhook/config"
	"alert-webhook/utils"
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	defaultAlertStoreCapacity = 1000
	defaultCompactMaxBytes    = 5 * 1024 * 1024 // 5 MB
)

// DeliveryResult records the outcome of sending an alert to one notification channel target.
type DeliveryResult struct {
	Client      string `json:"client"`
	Target      string `json:"target,omitempty"`
	Success     bool   `json:"success"`
	RateLimited bool   `json:"rate_limited,omitempty"`
	StatusCode  int    `json:"status_code,omitempty"`
	DurationMs  int64  `json:"duration_ms"`
	Batches     int    `json:"batches,omitempty"`
	Error       string `json:"error,omitempty"`
	Response    string `json:"response,omitempty"`
}

// AlertRecord captures a normalized alert event and its delivery outcomes.
type AlertRecord struct {
	ID            string            `json:"id"`
	Timestamp     string            `json:"timestamp"`
	Source        string            `json:"source"`         // "alertmanager" | "traffic" | "kafka" | "test"
	Status        string            `json:"status"`         // "firing" | "resolved"
	DeliveryState string            `json:"delivery_state"` // "sent" | "partial" | "failed" | "filtered" | "dry_run"
	AlertName     string            `json:"alert_name"`
	Severity      string            `json:"severity"`
	SeverityCode  string            `json:"severity_code"`
	Instance      string            `json:"instance"`
	Summary       string            `json:"summary"`
	Description   string            `json:"description"`
	Labels        map[string]string `json:"labels"`
	Annotations   map[string]string `json:"annotations"`
	FilterReason  string            `json:"filter_reason,omitempty"`
	Deliveries    []DeliveryResult  `json:"deliveries"`
	StartsAt      string            `json:"starts_at,omitempty"`
	EndsAt        string            `json:"ends_at,omitempty"`
}

// AlertQuery filters and paginates alert records.
type AlertQuery struct {
	Page          int
	PageSize      int
	Status        string
	DeliveryState string
	Source        string
	Severity      string
	Q             string
}

// AlertQueryResult is returned by AlertStore.Query.
type AlertQueryResult struct {
	Items    []AlertRecord `json:"items"`
	Total    int           `json:"total"`
	Page     int           `json:"page"`
	PageSize int           `json:"page_size"`
}

// SeverityStats aggregates alert counts by severity.
type SeverityStats struct {
	Emergency int `json:"emergency"`
	Critical  int `json:"critical"`
	Warning   int `json:"warning"`
	Info      int `json:"info"`
	Other     int `json:"other"`
}

// SourceStats aggregates alert counts by origin source.
type SourceStats struct {
	Alertmanager int `json:"alertmanager"`
	Traffic      int `json:"traffic"`
	Kafka        int `json:"kafka"`
	Test         int `json:"test"`
}

// ChannelStats aggregates delivery metrics per channel.
type ChannelStats struct {
	Enabled         bool    `json:"enabled"`
	Configured      bool    `json:"configured"`
	ConfiguredCount int     `json:"configured_count"`
	TotalCount      int     `json:"total_count"`
	Sent            int     `json:"sent"`
	Failed          int     `json:"failed"`
	AvgLatencyMs    float64 `json:"avg_latency_ms"`
}

// TimelineBucket aggregates hourly alert counts in Asia/Shanghai.
type TimelineBucket struct {
	Hour     string `json:"hour"`
	Firing   int    `json:"firing"`
	Resolved int    `json:"resolved"`
	Filtered int    `json:"filtered"`
}

// OverviewStats aggregates alert metrics for GET /api/overview.
type OverviewStats struct {
	Total       int                     `json:"total"`
	Firing      int                     `json:"firing"`
	Resolved    int                     `json:"resolved"`
	Sent        int                     `json:"sent"`
	Partial     int                     `json:"partial"`
	Failed      int                     `json:"failed"`
	Filtered    int                     `json:"filtered"`
	SuccessRate float64                 `json:"success_rate"`
	BySeverity  SeverityStats           `json:"by_severity"`
	BySource    SourceStats             `json:"by_source"`
	ByChannel   map[string]ChannelStats `json:"by_channel"`
	Timeline    []TimelineBucket        `json:"timeline"`
}

// AlertStore stores recent alert records in a thread-safe ring buffer backed by optional JSONL persistence.
type AlertStore struct {
	mu              sync.RWMutex
	capacity        int
	records         []AlertRecord
	filePath        string
	persist         bool
	seq             uint64
	diskLines       int
	diskBytes       int64
	compactMaxBytes int64
}

// NewAlertStore initializes an AlertStore with the given dataDir and ring capacity (defaults to 1000).
func NewAlertStore(dataDir string, capacity int) *AlertStore {
	if capacity <= 0 {
		capacity = defaultAlertStoreCapacity
	}
	s := &AlertStore{
		capacity:        capacity,
		records:         make([]AlertRecord, 0, min(capacity, 64)),
		compactMaxBytes: defaultCompactMaxBytes,
	}

	dir := strings.TrimSpace(dataDir)
	if dir == "" {
		return s
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Printf("[Warn] 无法创建数据目录 %s: %v，告警历史将仅保存在内存中", dir, err)
		return s
	}

	filePath := filepath.Join(dir, "alerts.jsonl")
	s.filePath = filePath
	s.persist = true
	s.loadFromFile()
	return s
}

func (s *AlertStore) compactMaxLines() int {
	return max(s.capacity*2, 10)
}

func (s *AlertStore) loadFromFile() {
	f, err := os.Open(s.filePath)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[Warn] 无法读取历史告警文件 %s: %v", s.filePath, err)
		}
		return
	}
	defer f.Close()

	if info, statErr := f.Stat(); statErr == nil {
		s.diskBytes = info.Size()
	}

	scanner := bufio.NewScanner(f)
	// Allow up to 1MB lines for large alert payloads
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	loaded := make([]AlertRecord, 0)
	lineNum := 0
	nonEmptyLines := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		nonEmptyLines++
		var rec AlertRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			log.Printf("[Warn] 跳过第 %d 行损坏的告警记录: %v", lineNum, err)
			continue
		}
		normalizeAlertRecord(&rec)
		loaded = append(loaded, rec)
	}
	if err := scanner.Err(); err != nil {
		log.Printf("[Warn] 读取历史告警文件异常: %v", err)
	}

	s.diskLines = nonEmptyLines
	if len(loaded) > s.capacity {
		loaded = loaded[len(loaded)-s.capacity:]
	}
	s.records = append(s.records, loaded...)

	if s.diskLines > s.compactMaxLines() || (s.compactMaxBytes > 0 && s.diskBytes > s.compactMaxBytes) {
		s.compactFileLocked()
	}
}

func normalizeAlertRecord(rec *AlertRecord) {
	if rec.Labels == nil {
		rec.Labels = map[string]string{}
	}
	if rec.Annotations == nil {
		rec.Annotations = map[string]string{}
	}
	if rec.Deliveries == nil {
		rec.Deliveries = []DeliveryResult{}
	}
	if rec.SeverityCode == "" && rec.Severity != "" {
		rec.SeverityCode = utils.MapSeverity(rec.Severity)
	}
}

func cloneAlertRecord(rec AlertRecord) AlertRecord {
	normalizeAlertRecord(&rec)
	labels := make(map[string]string, len(rec.Labels))
	for k, v := range rec.Labels {
		labels[k] = v
	}
	annotations := make(map[string]string, len(rec.Annotations))
	for k, v := range rec.Annotations {
		annotations[k] = v
	}
	deliveries := append([]DeliveryResult{}, rec.Deliveries...)
	rec.Labels = labels
	rec.Annotations = annotations
	rec.Deliveries = deliveries
	return rec
}

// Add appends an AlertRecord to the ring buffer and persists it to alerts.jsonl if enabled.
func (s *AlertStore) Add(rec AlertRecord) AlertRecord {
	res := s.AddBatch([]AlertRecord{rec})
	return res[0]
}

// AddBatch appends multiple AlertRecords under a single lock acquisition and single disk write/compaction.
func (s *AlertStore) AddBatch(recs []AlertRecord) []AlertRecord {
	if len(recs) == 0 {
		return nil
	}
	if len(recs) > s.capacity {
		recs = recs[len(recs)-s.capacity:]
	}

	loc := utils.GetShanghaiLocation()
	now := time.Now()
	nowStr := now.In(loc).Format(time.RFC3339)
	nowNano := now.UnixNano()

	s.mu.Lock()
	defer s.mu.Unlock()

	storedBatch := make([]AlertRecord, len(recs))
	out := make([]AlertRecord, len(recs))
	for i, rec := range recs {
		s.seq++
		if rec.ID == "" {
			rec.ID = fmt.Sprintf("alt-%d-%d", nowNano, s.seq)
		}
		if rec.Timestamp == "" {
			rec.Timestamp = nowStr
		}
		normalizeAlertRecord(&rec)
		stored := cloneAlertRecord(rec)
		storedBatch[i] = stored
		out[i] = cloneAlertRecord(stored)
	}

	total := len(s.records) + len(storedBatch)
	if total <= s.capacity {
		s.records = append(s.records, storedBatch...)
	} else {
		overflow := total - s.capacity
		if overflow >= len(s.records) {
			keep := storedBatch[len(storedBatch)-min(len(storedBatch), s.capacity):]
			s.records = append(s.records[:0], keep...)
		} else {
			remain := len(s.records) - overflow
			copy(s.records, s.records[overflow:])
			s.records = append(s.records[:remain], storedBatch...)
		}
	}

	if s.persist && s.filePath != "" {
		if s.diskLines+len(storedBatch) > s.compactMaxLines() {
			s.compactFileLocked()
		} else {
			var buf bytes.Buffer
			writtenLines := 0
			for _, rec := range storedBatch {
				data, err := json.Marshal(rec)
				if err != nil {
					log.Printf("[Warn] 序列化告警记录失败: %v", err)
					continue
				}
				buf.Write(data)
				buf.WriteByte('\n')
				writtenLines++
			}
			if writtenLines > 0 {
				if s.compactMaxBytes > 0 && s.diskBytes+int64(buf.Len()) > s.compactMaxBytes {
					s.compactFileLocked()
				} else {
					s.appendBytesLocked(buf.Bytes(), writtenLines)
				}
			}
		}
	}

	return out
}

func (s *AlertStore) appendBytesLocked(data []byte, lines int) {
	f, err := os.OpenFile(s.filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Printf("[Warn] 打开告警持久化文件 %s 失败: %v", s.filePath, err)
		return
	}
	defer f.Close()

	if _, err := f.Write(data); err != nil {
		log.Printf("[Warn] 写入告警持久化文件失败: %v", err)
		return
	}
	s.diskLines += lines
	s.diskBytes += int64(len(data))
}

func (s *AlertStore) compactFileLocked() {
	if !s.persist || s.filePath == "" {
		return
	}

	var buf bytes.Buffer
	lines := 0
	for _, rec := range s.records {
		data, err := json.Marshal(rec)
		if err != nil {
			log.Printf("[Warn] 压缩序列化告警记录失败: %v", err)
			continue
		}
		buf.Write(data)
		buf.WriteByte('\n')
		lines++
	}

	if err := writeFileAtomic(s.filePath, buf.Bytes()); err != nil {
		log.Printf("[Warn] 压缩告警持久化文件 %s 失败: %v", s.filePath, err)
		return
	}
	s.diskLines = lines
	s.diskBytes = int64(buf.Len())
}

func writeFileAtomic(targetPath string, data []byte) error {
	dir := filepath.Dir(targetPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	tmpFile, err := os.CreateTemp(dir, ".alerts-*.tmp")
	if err != nil {
		return os.WriteFile(targetPath, data, 0644)
	}
	tmpPath := tmpFile.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0644); err != nil {
		return err
	}

	if err := os.Rename(tmpPath, targetPath); err != nil {
		if errors.Is(err, syscall.EXDEV) || errors.Is(err, syscall.EBUSY) {
			return os.WriteFile(targetPath, data, 0644)
		}
		return err
	}
	cleanup = false
	return nil
}

// Query filters and paginates records in reverse-chronological order (newest first).
func (s *AlertStore) Query(q AlertQuery) AlertQueryResult {
	page := q.Page
	if page < 1 {
		page = 1
	}
	pageSize := q.PageSize
	if pageSize <= 0 {
		pageSize = 20
	}
	if pageSize > 200 {
		pageSize = 200
	}

	statusFilter := strings.ToLower(strings.TrimSpace(q.Status))
	stateFilter := strings.ToLower(strings.TrimSpace(q.DeliveryState))
	sourceFilter := strings.ToLower(strings.TrimSpace(q.Source))
	severityFilter := strings.ToLower(strings.TrimSpace(q.Severity))
	keyword := strings.ToLower(strings.TrimSpace(q.Q))

	start := (page - 1) * pageSize
	end := start + pageSize

	s.mu.RLock()
	items := make([]AlertRecord, 0, min(pageSize, len(s.records)))
	total := 0
	for i := len(s.records) - 1; i >= 0; i-- {
		rec := s.records[i]
		if statusFilter != "" && strings.ToLower(rec.Status) != statusFilter {
			continue
		}
		if stateFilter != "" && strings.ToLower(rec.DeliveryState) != stateFilter {
			continue
		}
		if sourceFilter != "" && strings.ToLower(rec.Source) != sourceFilter {
			continue
		}
		if severityFilter != "" &&
			strings.ToLower(rec.Severity) != severityFilter &&
			strings.ToLower(rec.SeverityCode) != severityFilter {
			continue
		}
		if keyword != "" && !matchesAlertKeyword(rec, keyword) {
			continue
		}
		if total >= start && total < end {
			items = append(items, cloneAlertRecord(rec))
		}
		total++
	}
	s.mu.RUnlock()

	return AlertQueryResult{
		Items:    items,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
	}
}

func matchesAlertKeyword(rec AlertRecord, keyword string) bool {
	fields := []string{
		rec.ID,
		rec.AlertName,
		rec.Severity,
		rec.SeverityCode,
		rec.Instance,
		rec.Summary,
		rec.Description,
		rec.Source,
		rec.FilterReason,
	}
	for _, f := range fields {
		if strings.Contains(strings.ToLower(f), keyword) {
			return true
		}
	}
	for k, v := range rec.Labels {
		if strings.Contains(strings.ToLower(k), keyword) || strings.Contains(strings.ToLower(v), keyword) {
			return true
		}
	}
	for _, d := range rec.Deliveries {
		if strings.Contains(strings.ToLower(d.Target), keyword) {
			return true
		}
	}
	return false
}

// Recent returns up to n most recent alert records (newest first).
func (s *AlertStore) Recent(n int) []AlertRecord {
	if n <= 0 {
		return []AlertRecord{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	count := min(n, len(s.records))
	out := make([]AlertRecord, 0, count)
	for i := len(s.records) - 1; i >= len(s.records)-count; i-- {
		out = append(out, cloneAlertRecord(s.records[i]))
	}
	return out
}

// Clear empties all stored records in memory and truncates the JSONL backing file.
func (s *AlertStore) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.records = s.records[:0]
	if s.persist && s.filePath != "" {
		if err := os.WriteFile(s.filePath, []byte{}, 0644); err != nil {
			log.Printf("[Warn] 清空持久化告警文件失败: %v", err)
			return err
		}
	}
	s.diskLines = 0
	s.diskBytes = 0
	return nil
}

// Stats aggregates alert, channel, severity, source, and 24-hour timeline metrics.
func (s *AlertStore) Stats(cfg *config.AppConfig) OverviewStats {
	return s.StatsAt(cfg, time.Now())
}

// StatsAt computes OverviewStats relative to reference time now (useful for deterministic timeline tests).
func (s *AlertStore) StatsAt(cfg *config.AppConfig, now time.Time) OverviewStats {
	loc := utils.GetShanghaiLocation()
	nowInLoc := now.In(loc)
	currentHour := time.Date(nowInLoc.Year(), nowInLoc.Month(), nowInLoc.Day(), nowInLoc.Hour(), 0, 0, 0, loc)

	// Prepare 24 hourly buckets in Asia/Shanghai
	timeline := make([]TimelineBucket, 24)
	bucketIndex := make(map[string]int, 24)
	for i := 0; i < 24; i++ {
		t := currentHour.Add(-time.Duration(23-i) * time.Hour)
		key := t.Format("2006-01-02 15")
		bucketIndex[key] = i
		timeline[i] = TimelineBucket{
			Hour: t.Format("15:00"),
		}
	}

	byChannel := make(map[string]ChannelStats, len(config.SupportedClients))
	channelLatencySum := make(map[string]int64, len(config.SupportedClients))
	channelLatencyCount := make(map[string]int, len(config.SupportedClients))

	for _, ch := range config.SupportedClients {
		enabled := cfg != nil && slices.Contains(cfg.Clients, ch)
		configuredCount := 0
		totalCount := 0
		if cfg != nil {
			if n, ok := cfg.Notifiers[ch]; ok {
				totalCount = len(n.AllTargets())
				configuredCount = len(n.ConfiguredTargets())
			}
		}
		byChannel[ch] = ChannelStats{
			Enabled:         enabled,
			Configured:      configuredCount > 0,
			ConfiguredCount: configuredCount,
			TotalCount:      totalCount,
		}
	}

	s.mu.RLock()
	records := append([]AlertRecord(nil), s.records...)
	s.mu.RUnlock()

	stats := OverviewStats{
		Total:     len(records),
		ByChannel: byChannel,
		Timeline:  timeline,
	}

	var deliveryAttempts int
	var deliverySuccesses int

	for _, rec := range records {
		switch strings.ToLower(rec.Status) {
		case "firing":
			stats.Firing++
		case "resolved":
			stats.Resolved++
		}

		switch strings.ToLower(rec.DeliveryState) {
		case "sent":
			stats.Sent++
		case "partial":
			stats.Partial++
		case "failed":
			stats.Failed++
		case "filtered":
			stats.Filtered++
		}

		switch strings.ToLower(rec.Severity) {
		case "emergency":
			stats.BySeverity.Emergency++
		case "critical":
			stats.BySeverity.Critical++
		case "warning":
			stats.BySeverity.Warning++
		case "info":
			stats.BySeverity.Info++
		default:
			stats.BySeverity.Other++
		}

		switch strings.ToLower(rec.Source) {
		case "alertmanager":
			stats.BySource.Alertmanager++
		case "traffic":
			stats.BySource.Traffic++
		case "kafka":
			stats.BySource.Kafka++
		case "test":
			stats.BySource.Test++
		}

		for _, d := range rec.Deliveries {
			if d.Client == "" {
				continue
			}
			deliveryAttempts++
			if d.Success {
				deliverySuccesses++
			}
			cs, ok := stats.ByChannel[d.Client]
			if !ok {
				continue
			}
			if d.Success {
				cs.Sent++
			} else {
				cs.Failed++
			}
			stats.ByChannel[d.Client] = cs
			if d.Success || d.StatusCode > 0 || d.DurationMs > 0 {
				channelLatencySum[d.Client] += d.DurationMs
				channelLatencyCount[d.Client]++
			}
		}

		if ts, err := time.Parse(time.RFC3339, rec.Timestamp); err == nil {
			key := ts.In(loc).Format("2006-01-02 15")
			if idx, ok := bucketIndex[key]; ok {
				if strings.EqualFold(rec.DeliveryState, "filtered") {
					stats.Timeline[idx].Filtered++
				} else if strings.EqualFold(rec.Status, "resolved") {
					stats.Timeline[idx].Resolved++
				} else {
					stats.Timeline[idx].Firing++
				}
			}
		}
	}

	for _, ch := range config.SupportedClients {
		if cnt := channelLatencyCount[ch]; cnt > 0 {
			cs := stats.ByChannel[ch]
			cs.AvgLatencyMs = math.Round((float64(channelLatencySum[ch])/float64(cnt))*10) / 10
			stats.ByChannel[ch] = cs
		}
	}

	if deliveryAttempts > 0 {
		stats.SuccessRate = math.Round((float64(deliverySuccesses)/float64(deliveryAttempts))*1000) / 10
	} else if stats.Sent > 0 {
		stats.SuccessRate = 100
	}

	return stats
}
