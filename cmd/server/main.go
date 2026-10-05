package main

import (
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"

	"discountguard/internal/app"
	"discountguard/internal/store"
)

//go:embed all:web
var webFS embed.FS

func main() {
	dbPath := env("DG_DB", filepath.Join("data", "app.db"))
	addr := env("DG_ADDR", ":8080")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		log.Fatalf("mkdir: %v", err)
	}
	db, err := store.Open(dbPath)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	if err := store.Seed(db); err != nil {
		log.Fatalf("seed: %v", err)
	}

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())

	svc := app.NewService(db)
	app.RegisterRoutes(r, svc)

	registerFrontend(r)
	log.Printf("折扣码封禁与订单追溯系统已启动: http://localhost%s", addr)
	if err := r.Run(addr); err != nil {
		log.Fatal(err)
	}
}

func registerFrontend(r *gin.Engine) {
	dist, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatalf("web fs: %v", err)
	}
	serveFile := func(c *gin.Context, name string) bool {
		data, err := fs.ReadFile(dist, name)
		if err != nil {
			return false
		}
		c.Data(http.StatusOK, mimeFor(name), data)
		return true
	}
	r.NoRoute(func(c *gin.Context) {
		urlPath := c.Request.URL.Path
		if strings.HasPrefix(urlPath, "/api/") {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		p := strings.TrimPrefix(urlPath, "/")
		if p == "" {
			p = "index.html"
		}
		// 命中真实构建产物（assets 等）直接返回
		if serveFile(c, p) {
			return
		}
		// 其余非 API 路径一律回退到 SPA 入口
		if serveFile(c, "index.html") {
			return
		}
		c.String(http.StatusOK, "API 已启动（前端尚未构建）。接口前缀 /api")
	})
}

func mimeFor(name string) string {
	switch {
	case strings.HasSuffix(name, ".html"):
		return "text/html; charset=utf-8"
	case strings.HasSuffix(name, ".js"):
		return "text/javascript; charset=utf-8"
	case strings.HasSuffix(name, ".css"):
		return "text/css; charset=utf-8"
	case strings.HasSuffix(name, ".svg"):
		return "image/svg+xml"
	case strings.HasSuffix(name, ".json"):
		return "application/json"
	default:
		return "application/octet-stream"
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
