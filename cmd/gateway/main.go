package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"cpa-relay-gateway/pkg/config"
	"cpa-relay-gateway/pkg/server"
)

func main() {
	configPath := flag.String("c", "config.yaml", "path to configuration file")
	flag.Parse()

	log.Printf("[INFO] starting cpa-relay-gateway with config: %s", *configPath)

	cfgMgr := config.NewManager()
	if err := cfgMgr.Load(*configPath); err != nil {
		log.Fatalf("[FATAL] failed to load configuration: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 启动后台配置监听与原子热重载
	if err := cfgMgr.StartWatcher(ctx, *configPath); err != nil {
		log.Printf("[WARN] failed to start config watcher: %v", err)
	} else {
		log.Printf("[INFO] config watcher started on %s", *configPath)
	}

	cfg := cfgMgr.Get()
	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	if cfg.Server.Port == 0 {
		addr = ":8787"
	}

	srv := server.NewServer(cfgMgr)
	httpServer := &http.Server{
		Addr:    addr,
		Handler: srv.Handler(),
	}

	go func() {
		log.Printf("[INFO] gateway listening on http://%s", addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[FATAL] listen failed: %v", err)
		}
	}()

	// 优雅关闭信号监听
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("[INFO] shutting down gateway...")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("[ERROR] server forced shutdown: %v", err)
	}
	log.Println("[INFO] gateway stopped cleanly.")
}
