// Command server 是 openclash-sub-converter 的 HTTP 服务入口。
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/yangyu/openclash-sub-converter/internal/api"
	"github.com/yangyu/openclash-sub-converter/internal/config"
	"github.com/yangyu/openclash-sub-converter/internal/fetcher"
	"github.com/yangyu/openclash-sub-converter/internal/store"
)

func main() {
	// 配置：文件不存在时使用默认值；环境变量可覆盖（OSC_PORT 等）。
	cfg, err := config.Load("config/config.yaml")
	if err != nil {
		slog.Error("load config failed", "error", err)
		os.Exit(1)
	}
	cfg.ApplyEnv()
	setupLogger(cfg.Logging.Level)

	// 管理台数据层：数据目录创建失败属环境问题（磁盘只读/权限不足等），
	// 直接退出并给出明确消息，避免服务跑起来却无法持久化。
	st, err := store.New(cfg.Server.DataDir)
	if err != nil {
		slog.Error("init store failed", "data_dir", cfg.Server.DataDir, "error", err)
		os.Exit(1)
	}

	f := fetcher.New(cfg.Fetcher)
	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Server.Port),
		Handler: api.NewServer(cfg, f, st),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		slog.Info("listening on " + srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server error", "error", err)
			stop()
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down gracefully")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("graceful shutdown failed", "error", err)
	}
	slog.Info("server stopped")
}

// setupLogger 按配置级别设置 slog 默认 logger。
func setupLogger(level string) {
	var lv slog.Level
	switch level {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lv})))
}
