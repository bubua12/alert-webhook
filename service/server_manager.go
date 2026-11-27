package service

import (
	"alert-webhook/config"
	"alert-webhook/store"
	"context"
	"crypto/subtle"
	"errors"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

var (
	uiFSMu sync.RWMutex
	uiFS   fs.FS
)

// RegisterUIFS registers the embedded frontend filesystem served by ServerManager.
func RegisterUIFS(f fs.FS) {
	uiFSMu.Lock()
	uiFS = f
	uiFSMu.Unlock()
}

func getUIFS() fs.FS {
	uiFSMu.RLock()
	f := uiFS
	uiFSMu.RUnlock()
	return f
}

// ServerManager HTTP服务器管理器 用于启动监听端口，接收AlertManager推送与控制台API请求
type ServerManager struct {
	mu         sync.Mutex
	server     *http.Server
	cfgMgr     *config.ConfigManager
	alertStore *store.AlertStore
	logBuffer  *store.LogBuffer
	svcMgr     *ServiceManager
	dispatcher *WebhookDispatcher
	startedAt  time.Time
}

// NewServerManager 创建服务器管理器
func NewServerManager(cfgMgr *config.ConfigManager, alertStore *store.AlertStore, logBuffer *store.LogBuffer, svcMgr *ServiceManager) *ServerManager {
	return &ServerManager{
		cfgMgr:     cfgMgr,
		alertStore: alertStore,
		logBuffer:  logBuffer,
		svcMgr:     svcMgr,
		dispatcher: NewWebhookDispatcher(cfgMgr, alertStore),
		startedAt:  time.Now(),
	}
}

// extractAPIKey retrieves the API key/token from X-API-Key, X-API-Token, or Authorization: Bearer.
func extractAPIKey(c *gin.Context) string {
	for _, header := range []string{"X-API-Key", "X-API-Token"} {
		if v := strings.TrimSpace(c.GetHeader(header)); v != "" {
			return v
		}
	}
	authHeader := strings.TrimSpace(c.GetHeader("Authorization"))
	if len(authHeader) >= 7 && strings.EqualFold(authHeader[:7], "Bearer ") {
		return strings.TrimSpace(authHeader[7:])
	}
	return ""
}

// AuthMiddleware protects /api/* endpoints when server.auth_token (or API_KEY) is configured.
func AuthMiddleware(cfgMgr *config.ConfigManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		if cfgMgr == nil {
			c.Next()
			return
		}
		expected := cfgMgr.Get().Server.AuthToken
		if expected == "" {
			c.Next()
			return
		}

		provided := extractAPIKey(c)
		if subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error":         "unauthorized",
				"auth_required": true,
			})
			return
		}
		c.Next()
	}
}

// BuildRouter constructs the Gin engine with all webhook, health, REST API, and static UI routes.
func (sm *ServerManager) BuildRouter() *gin.Engine {
	if os.Getenv("GIN_MODE") != gin.DebugMode {
		gin.SetMode(gin.ReleaseMode)
	}

	router := gin.New()
	router.Use(gin.Recovery())

	apiHandler := NewAPIHandler(sm.cfgMgr, sm.alertStore, sm.logBuffer, sm.svcMgr, sm.startedAt)

	dispatcher := sm.dispatcher
	if dispatcher == nil {
		dispatcher = NewWebhookDispatcher(sm.cfgMgr, sm.alertStore)
	}

	// Public operational, auth handshake, & webhook endpoints (never gated by AuthMiddleware)
	router.POST("/webhook-alert", dispatcher.Handle)
	router.GET("/healthz", apiHandler.HandleHealthz)
	router.GET("/readyz", apiHandler.HandleReadyz)
	router.GET("/api/auth/status", apiHandler.HandleAuthStatus)
	router.POST("/api/auth/login", apiHandler.HandleAuthLogin)

	// Control-plane REST API
	api := router.Group("/api")
	api.Use(AuthMiddleware(sm.cfgMgr))
	{
		api.GET("/overview", apiHandler.HandleOverview)
		api.GET("/alerts", apiHandler.HandleListAlerts)
		api.DELETE("/alerts", apiHandler.HandleClearAlerts)
		api.POST("/alerts/clear", apiHandler.HandleClearAlerts)
		api.GET("/config", apiHandler.HandleGetConfig)
		api.PUT("/config", apiHandler.HandleUpdateConfig)
		api.POST("/config", apiHandler.HandleUpdateConfig)
		api.PUT("/config/yaml", apiHandler.HandleUpdateConfigYAML)
		api.POST("/config/yaml", apiHandler.HandleUpdateConfigYAML)
		api.POST("/config/reload", apiHandler.HandleReloadConfig)
		api.POST("/test/notifier", apiHandler.HandleTestNotifier)
		api.POST("/test/clickhouse", apiHandler.HandleTestClickHouse)
		api.POST("/test/kafka", apiHandler.HandleTestKafka)
		api.POST("/filter/test", apiHandler.HandleTestFilter)
		api.POST("/simulate", apiHandler.HandleSimulate)
		api.POST("/services/traffic/check", apiHandler.HandleTriggerTrafficCheck)
		api.POST("/services/kafka/check", apiHandler.HandleTriggerKafkaCheck)
		api.GET("/logs", apiHandler.HandleGetLogs)
	}

	// Embedded Web UI assets (never use http.FileServer at '/' to avoid redirect loops)
	router.GET("/", func(c *gin.Context) {
		sm.serveIndex(c)
	})
	router.GET("/css/*filepath", func(c *gin.Context) {
		sm.serveStaticSubdir(c, "css", c.Param("filepath"))
	})
	router.GET("/js/*filepath", func(c *gin.Context) {
		sm.serveStaticSubdir(c, "js", c.Param("filepath"))
	})
	router.GET("/favicon.ico", func(c *gin.Context) {
		if !sm.isUIActive() {
			c.Status(http.StatusNotFound)
			return
		}
		if data, err := fs.ReadFile(getUIFS(), "favicon.ico"); err == nil {
			c.Data(http.StatusOK, "image/x-icon", data)
			return
		}
		c.Status(http.StatusNoContent)
	})

	router.NoRoute(func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/api/") || c.Request.URL.Path == "/api" {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		if sm.isUIActive() {
			sm.serveIndex(c)
			return
		}
		c.Status(http.StatusNotFound)
	})

	return router
}

func (sm *ServerManager) isUIActive() bool {
	if getUIFS() == nil {
		return false
	}
	if sm.cfgMgr != nil {
		return sm.cfgMgr.Get().Server.IsUIEnabled()
	}
	return true
}

func (sm *ServerManager) serveIndex(c *gin.Context) {
	if !sm.isUIActive() {
		c.Status(http.StatusNotFound)
		return
	}
	data, err := fs.ReadFile(getUIFS(), "index.html")
	if err != nil {
		c.String(http.StatusInternalServerError, "无法加载控制台页面")
		return
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", data)
}

func (sm *ServerManager) serveStaticSubdir(c *gin.Context, subdir, rawSubpath string) {
	if !sm.isUIActive() {
		c.Status(http.StatusNotFound)
		return
	}

	if strings.Contains(rawSubpath, "..") {
		c.Status(http.StatusNotFound)
		return
	}
	cleaned := path.Clean("/" + rawSubpath)
	rel := strings.TrimPrefix(cleaned, "/")
	if rel == "" || rel == "." {
		c.Status(http.StatusNotFound)
		return
	}

	assetPath := path.Join(subdir, rel)
	data, err := fs.ReadFile(getUIFS(), assetPath)
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}

	var contentType string
	switch strings.ToLower(path.Ext(assetPath)) {
	case ".css":
		contentType = "text/css; charset=utf-8"
	case ".js", ".mjs":
		contentType = "application/javascript; charset=utf-8"
	default:
		contentType = mime.TypeByExtension(path.Ext(assetPath))
		if contentType == "" {
			contentType = "application/octet-stream"
		}
	}
	c.Data(http.StatusOK, contentType, data)
}

// StartWebhookServer 启动webhook与控制台HTTP服务器
func (sm *ServerManager) StartWebhookServer(addr string) error {
	router := sm.BuildRouter()

	srv := &http.Server{
		Addr:              addr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	sm.mu.Lock()
	sm.server = srv
	sm.mu.Unlock()

	log.Printf("Alert Webhook服务启动于 %s\n", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Shutdown 优雅关闭服务器（5秒超时）
func (sm *ServerManager) Shutdown() error {
	sm.mu.Lock()
	srv := sm.server
	dispatcher := sm.dispatcher
	sm.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var srvErr error
	if srv != nil {
		log.Println("正在关闭HTTP服务器...")
		srvErr = srv.Shutdown(ctx)
	}
	if dispatcher != nil {
		dispatcher.Shutdown(ctx)
	}
	return srvErr
}
