package main

import (
	"context"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/DEEIX-AI/DEEIX-Chat/demo/go-mcp-server/internal/config"
	"github.com/DEEIX-AI/DEEIX-Chat/demo/go-mcp-server/internal/server"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load(os.LookupEnv)
	if err != nil {
		logger.Error("configuration failed", "error", err)
		os.Exit(1)
	}

	listener, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		logger.Error("listen failed", "addr", cfg.Addr, "error", err)
		os.Exit(1)
	}

	app, err := server.New(cfg, logger, time.Now)
	if err != nil {
		_ = listener.Close()
		logger.Error("server initialization failed", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("mcp demo server starting", "addr", listener.Addr().String())
	if err := app.Serve(ctx, listener); err != nil {
		logger.Error("mcp demo server stopped with error", "error", err)
		os.Exit(1)
	}
	logger.Info("mcp demo server stopped")
}
