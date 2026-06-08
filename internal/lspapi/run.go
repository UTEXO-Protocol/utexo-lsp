package lspapi

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"utexo-lsp/pkg/node_client"
)

// Run starts the API server and cron workers using environment-based config.
func Run() {
	cfg := LoadConfig()
	if err := cfg.Validate(); err != nil {
		log.Fatalf("config validation failed: %v", err)
	}

	store, err := NewStore(cfg)
	if err != nil {
		log.Fatalf("db init failed: %v", err)
	}
	defer store.Close()

	api := NewAPI(
		cfg,
		store,
		node_client.NewClient(cfg.LSPBaseURL, cfg.LSPToken, &http.Client{Timeout: cfg.HTTPTimeout}),
		node_client.NewClient(cfg.RGBNodeBaseURL, cfg.RGBNodeToken, &http.Client{Timeout: cfg.HTTPTimeout}),
	)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	go api.runCron(ctx)

	srv := &http.Server{
		Addr:         cfg.ServerAddr,
		Handler:      api.routes(),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 20 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer shutdownCancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Printf("utexo-lsp listening on %s", cfg.ServerAddr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server failed: %v", err)
	}
}
