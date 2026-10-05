package main

import (
	"embed"
	"flag"
	"io"
	"io/fs"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"promoleak/internal/api"
	"promoleak/internal/service"
	"promoleak/internal/store"
)

//go:embed all:web
var webFS embed.FS

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	dbPath := flag.String("db", "promoleak.db", "sqlite database path")
	flag.Parse()

	db, err := store.Open(*dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	if err := store.Seed(db); err != nil {
		log.Fatalf("seed: %v", err)
	}
	svc := service.New(db)

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	api.New(svc).Register(r)

	sub, _ := fs.Sub(webFS, "web")
	r.NoRoute(func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/api/") {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		path := strings.TrimPrefix(c.Request.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		f, err := sub.Open(path)
		if err != nil {
			// 非 API 且文件不存在：SPA 回退首页
			path = "index.html"
			f, err = sub.Open(path)
			if err != nil {
				c.Status(http.StatusInternalServerError)
				return
			}
		}
		defer f.Close()
		data, err := io.ReadAll(f)
		if err != nil {
			c.Status(http.StatusInternalServerError)
			return
		}
		c.Data(http.StatusOK, mimeFor(path), data)
	})

	log.Printf("门店折扣码系统已启动: http://localhost%s", *addr)
	if err := r.Run(*addr); err != nil {
		log.Fatal(err)
	}
}

func mimeFor(name string) string {
	switch {
	case strings.HasSuffix(name, ".html"):
		return "text/html; charset=utf-8"
	case strings.HasSuffix(name, ".js"):
		return "text/javascript; charset=utf-8"
	case strings.HasSuffix(name, ".css"):
		return "text/css; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}
