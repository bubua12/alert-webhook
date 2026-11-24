package service

import (
	"alert-webhook/config"
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

const clickhouseQueryTimeout = 15 * time.Second

// NginxAccessLog Nginx访问日志结构体
type NginxAccessLog struct {
	Timestamp           time.Time `ch:"timestamp"`
	ServerIP            string    `ch:"server_ip"`
	Domain              string    `ch:"domain"`
	RequestMethod       string    `ch:"request_method"`
	Status              int32     `ch:"status"`
	TopPath             string    `ch:"top_path"`
	Path                string    `ch:"path"`
	Query               string    `ch:"query"`
	Protocol            string    `ch:"protocol"`
	Referer             string    `ch:"referer"`
	UpstreamHost        string    `ch:"upstreamhost"`
	ResponseTime        float32   `ch:"responsetime"`
	UpstreamTime        float32   `ch:"upstreamtime"`
	Duration            float32   `ch:"duration"`
	RequestLength       int32     `ch:"request_length"`
	ResponseLength      int32     `ch:"response_length"`
	ClientIP            string    `ch:"client_ip"`
	ClientLatitude      float32   `ch:"client_latitude"`
	ClientLongitude     float32   `ch:"client_longitude"`
	RemoteUser          string    `ch:"remote_user"`
	RemoteIP            string    `ch:"remote_ip"`
	XFF                 string    `ch:"xff"`
	ClientCity          string    `ch:"client_city"`
	ClientRegion        string    `ch:"client_region"`
	ClientCountry       string    `ch:"client_country"`
	HTTPUserAgent       string    `ch:"http_user_agent"`
	ClientBrowserFamily string    `ch:"client_browser_family"`
	ClientBrowserMajor  string    `ch:"client_browser_major"`
	ClientOSFamily      string    `ch:"client_os_family"`
	ClientOSMajor       string    `ch:"client_os_major"`
	ClientDeviceBrand   string    `ch:"client_device_brand"`
	ClientDeviceModel   string    `ch:"client_device_model"`
	CreatedTime         time.Time `ch:"createdtime"`
}

// TrafficStats 流量统计结果
type TrafficStats struct {
	TotalCount         uint64  `ch:"total_count"`
	AvgRequestSize     float64 `ch:"avg_request_size"`
	AvgResponseSize    float64 `ch:"avg_response_size"`
	MaxRequestSize     int32   `ch:"max_request_size"`
	MaxResponseSize    int32   `ch:"max_response_size"`
	LargeRequestCount  uint64  `ch:"large_request_count"`
	LargeResponseCount uint64  `ch:"large_response_count"`
	Domain             string  `ch:"domain"`
	TopPath            string  `ch:"top_path"`
}

// ClickHouseService ClickHouse服务结构体
type ClickHouseService struct {
	conn   clickhouse.Conn
	config *config.AppConfig
	dbName string
}

func resolveClickHouseDB(db string) (string, error) {
	trimmed := strings.TrimSpace(db)
	if trimmed == "" {
		trimmed = "nginxlogs"
	}
	if !config.IsValidClickHouseIdentifier(trimmed) {
		return "", fmt.Errorf("非法的 ClickHouse 数据库名称: %q（仅允许字母、数字、下划线）", trimmed)
	}
	return trimmed, nil
}

// NewClickHouseService 创建新的ClickHouse服务实例
func NewClickHouseService(cfg *config.AppConfig) (*ClickHouseService, error) {
	if cfg == nil {
		return nil, fmt.Errorf("ClickHouse 配置不能为空")
	}
	dbName, err := resolveClickHouseDB(cfg.ClickHouse.Database)
	if err != nil {
		return nil, err
	}

	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr: []string{fmt.Sprintf("%s:%d", cfg.ClickHouse.Host, cfg.ClickHouse.Port)},
		Auth: clickhouse.Auth{
			Database: dbName,
			Username: cfg.ClickHouse.Username,
			Password: cfg.ClickHouse.Password,
		},
		Settings: clickhouse.Settings{
			"max_execution_time": 60,
		},
		DialTimeout:      10 * time.Second,
		MaxOpenConns:     5,
		MaxIdleConns:     5,
		ConnMaxLifetime:  10 * time.Minute,
		ConnOpenStrategy: clickhouse.ConnOpenInOrder,
	})
	if err != nil {
		return nil, fmt.Errorf("连接ClickHouse失败: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), clickhouseQueryTimeout)
	defer cancel()

	if err := conn.Ping(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("ClickHouse连接测试失败: %w", err)
	}

	log.Printf("ClickHouse连接成功: %s:%d/%s", cfg.ClickHouse.Host, cfg.ClickHouse.Port, dbName)

	return &ClickHouseService{
		conn:   conn,
		config: cfg.Clone(),
		dbName: dbName,
	}, nil
}

// Close 关闭ClickHouse连接
func (c *ClickHouseService) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// tableName returns the safely backtick-quoted <db>.nginx_access table reference.
func (c *ClickHouseService) tableName() string {
	return fmt.Sprintf("`%s`.nginx_access", c.dbName)
}

// CheckTrafficAnomalies 检查流量异常
func (c *ClickHouseService) CheckTrafficAnomalies() ([]TrafficStats, error) {
	ctx, cancel := context.WithTimeout(context.Background(), clickhouseQueryTimeout)
	defer cancel()

	timeWindow := c.config.TrafficAlert.TimeWindow
	requestThreshold := c.config.TrafficAlert.RequestSizeThreshold
	responseThreshold := c.config.TrafficAlert.ResponseSizeThreshold
	countThreshold := c.config.TrafficAlert.CountThreshold

	query := fmt.Sprintf(`
		SELECT 
			domain,
			top_path,
			count(*) as total_count,
			avg(request_length) as avg_request_size,
			avg(response_length) as avg_response_size,
			max(request_length) as max_request_size,
			max(response_length) as max_response_size,
			countIf(request_length > ?) as large_request_count,
			countIf(response_length > ?) as large_response_count
		FROM %s 
		WHERE timestamp >= now() - INTERVAL ? MINUTE
		GROUP BY domain, top_path
		HAVING large_request_count >= ? OR large_response_count >= ?
		ORDER BY total_count DESC
		LIMIT 100
	`, c.tableName())

	rows, err := c.conn.Query(ctx, query,
		requestThreshold,
		responseThreshold,
		timeWindow,
		countThreshold,
		countThreshold,
	)
	if err != nil {
		return nil, fmt.Errorf("查询流量异常数据失败: %w", err)
	}
	defer func(rows driver.Rows) {
		if err := rows.Close(); err != nil {
			log.Printf("[CheckTrafficAnomalies] rows.Close() 异常: %v", err)
		}
	}(rows)

	var results []TrafficStats
	for rows.Next() {
		var stat TrafficStats
		if err := rows.Scan(
			&stat.Domain,
			&stat.TopPath,
			&stat.TotalCount,
			&stat.AvgRequestSize,
			&stat.AvgResponseSize,
			&stat.MaxRequestSize,
			&stat.MaxResponseSize,
			&stat.LargeRequestCount,
			&stat.LargeResponseCount,
		); err != nil {
			log.Printf("扫描查询结果失败: %v", err)
			continue
		}
		results = append(results, stat)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历查询结果失败: %w", err)
	}

	return results, nil
}

// GetRecentLargeRequests 获取最近的大请求详情
func (c *ClickHouseService) GetRecentLargeRequests(domain, topPath string, limit int) ([]NginxAccessLog, error) {
	ctx, cancel := context.WithTimeout(context.Background(), clickhouseQueryTimeout)
	defer cancel()

	requestThreshold := c.config.TrafficAlert.RequestSizeThreshold
	responseThreshold := c.config.TrafficAlert.ResponseSizeThreshold
	timeWindow := c.config.TrafficAlert.TimeWindow

	query := fmt.Sprintf(`
		SELECT 
			timestamp, server_ip, domain, request_method, status, top_path, path,
			request_length, response_length, client_ip, responsetime
		FROM %s 
		WHERE timestamp >= now() - INTERVAL ? MINUTE
			AND domain = ?
			AND top_path = ?
			AND (request_length > ? OR response_length > ?)
		ORDER BY timestamp DESC
		LIMIT ?
	`, c.tableName())

	rows, err := c.conn.Query(ctx, query,
		timeWindow,
		domain,
		topPath,
		requestThreshold,
		responseThreshold,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("查询大请求详情失败: %w", err)
	}
	defer func(rows driver.Rows) {
		if err := rows.Close(); err != nil {
			log.Printf("[GetRecentLargeRequests] rows.Close() 异常: %v", err)
		}
	}(rows)

	var results []NginxAccessLog
	for rows.Next() {
		var nginxLog NginxAccessLog
		if err := rows.Scan(
			&nginxLog.Timestamp,
			&nginxLog.ServerIP,
			&nginxLog.Domain,
			&nginxLog.RequestMethod,
			&nginxLog.Status,
			&nginxLog.TopPath,
			&nginxLog.Path,
			&nginxLog.RequestLength,
			&nginxLog.ResponseLength,
			&nginxLog.ClientIP,
			&nginxLog.ResponseTime,
		); err != nil {
			continue
		}
		results = append(results, nginxLog)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历大请求详情失败: %w", err)
	}

	return results, nil
}

// TestConnection 测试ClickHouse连接
func (c *ClickHouseService) TestConnection() error {
	ctx, cancel := context.WithTimeout(context.Background(), clickhouseQueryTimeout)
	defer cancel()

	count, err := c.TestConnectionWithCount(ctx)
	if err != nil {
		return err
	}
	log.Printf("ClickHouse连接测试成功，最近1分钟有 %d 条记录", count)
	return nil
}

// TestConnectionWithCount queries recent row count in `<db>.nginx_access` and returns it.
func (c *ClickHouseService) TestConnectionWithCount(ctx context.Context) (uint64, error) {
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), clickhouseQueryTimeout)
		defer cancel()
	}

	var count uint64
	query := fmt.Sprintf("SELECT count(*) FROM %s WHERE timestamp >= now() - INTERVAL 1 MINUTE", c.tableName())
	if err := c.conn.QueryRow(ctx, query).Scan(&count); err != nil {
		return 0, fmt.Errorf("ClickHouse连接测试失败: %w", err)
	}
	return count, nil
}
