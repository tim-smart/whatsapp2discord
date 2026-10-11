// Command whatsapp2discord receives WhatsApp Cloud API webhooks and forwards
// incoming messages to a Discord channel via a Discord webhook.
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
)

type config struct {
	addr              string
	verifyToken       string
	appSecret         string
	discordWebhookURL string
	messagePrefix     string
}

func loadConfig() (config, error) {
	cfg := config{
		addr:              os.Getenv("ADDR"),
		verifyToken:       os.Getenv("WHATSAPP_VERIFY_TOKEN"),
		appSecret:         os.Getenv("WHATSAPP_APP_SECRET"),
		discordWebhookURL: os.Getenv("DISCORD_WEBHOOK_URL"),
		messagePrefix:     os.Getenv("MESSAGE_PREFIX"),
	}
	if cfg.addr == "" {
		cfg.addr = ":8080"
	}
	var missing []string
	if cfg.verifyToken == "" {
		missing = append(missing, "WHATSAPP_VERIFY_TOKEN")
	}
	if cfg.appSecret == "" {
		missing = append(missing, "WHATSAPP_APP_SECRET")
	}
	if cfg.discordWebhookURL == "" {
		missing = append(missing, "DISCORD_WEBHOOK_URL")
	}
	if len(missing) > 0 {
		return cfg, fmt.Errorf("missing required environment variables: %v", missing)
	}
	return cfg, nil
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	cfg, err := loadConfig()
	if err != nil {
		logger.Error("invalid configuration", "err", err)
		os.Exit(1)
	}

	handler := &webhookHandler{
		verifyToken: cfg.verifyToken,
		appSecret:   []byte(cfg.appSecret),
		discord: &discordClient{
			webhookURL:    cfg.discordWebhookURL,
			messagePrefix: cfg.messagePrefix,
			http:          &http.Client{Timeout: 10 * time.Second},
		},
		seen:   newDedupe(24 * time.Hour),
		logger: logger,
	}

	mux := http.NewServeMux()
	mux.Handle("/webhook", handler)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok"))
	})

	srv := &http.Server{
		Addr:              cfg.addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("listening", "addr", cfg.addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown failed", "err", err)
	}
}
