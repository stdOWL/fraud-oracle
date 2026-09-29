// Command api serves GET /score, /metrics and /healthz.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"fraud-oracle/service/internal/api"
	"fraud-oracle/service/internal/rules"
	"fraud-oracle/service/internal/store"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	dsn := os.Getenv("DATABASE_URL")
	listen := os.Getenv("LISTEN")
	sanctionsFile := os.Getenv("SANCTIONS_FILE")
	if dsn == "" || sanctionsFile == "" {
		log.Error("DATABASE_URL and SANCTIONS_FILE are required")
		os.Exit(2)
	}
	if listen == "" {
		listen = ":8080"
	}

	st, err := store.Open(ctx, dsn)
	if err != nil {
		log.Error("store", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	sanctions, err := rules.LoadSanctions(sanctionsFile, 3)
	if err != nil {
		log.Error("sanctions", "err", err)
		os.Exit(1)
	}
	log.Info("sanctions list loaded", "addresses", sanctions.Size())

	srv := &http.Server{
		Addr:              listen,
		Handler:           api.New(st, []rules.Rule{rules.DefaultPeelChain(), sanctions}, log),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	log.Info("api listening", "addr", listen)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("api", "err", err)
		os.Exit(1)
	}
}
