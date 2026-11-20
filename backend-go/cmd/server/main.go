package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"deploy-platform/backend-go/internal/config"
	"deploy-platform/backend-go/internal/httpapi"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	cfg := config.Load()
	if len(os.Args) > 1 {
		if os.Args[1] != "check-credentials" || len(os.Args) != 2 {
			return fmt.Errorf("用法: %s [check-credentials]", os.Args[0])
		}
		if err := httpapi.ValidateStoredCredentials(cfg); err != nil {
			return fmt.Errorf("凭据预检失败: %w", err)
		}
		log.Printf("当前配置中的凭据密钥与数据库中需要解密的凭据匹配")
		return nil
	}

	app, err := httpapi.NewApplication(cfg)
	if err != nil {
		return fmt.Errorf("启动失败: %w", err)
	}

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           app.Engine,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	serverErr := make(chan error, 1)
	go func() {
		log.Printf("deploy platform API listening on %s (env=%s, mock=%v)", cfg.HTTPAddr, cfg.Env, cfg.MockRuntime)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	var result error
	select {
	case <-ctx.Done():
	case err := <-serverErr:
		log.Printf("HTTP 服务异常: %v", err)
		result = err
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && result == nil {
		result = fmt.Errorf("HTTP 关闭异常: %w", err)
	}
	if err := app.Close(); err != nil && result == nil {
		result = fmt.Errorf("资源关闭异常: %w", err)
	}
	return result
}
