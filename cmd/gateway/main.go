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
	"weagent/backend/internal/gateway"
)

func main() {
	if e := run(); e != nil {
		slog.Error("喵连 Gateway exited", "reason", e.Error())
		os.Exit(1)
	}
}
func run() error {
	cfg, e := gateway.LoadConfig()
	if e != nil {
		return e
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	dbctx, c := context.WithTimeout(ctx, 10*time.Second)
	db, e := gateway.OpenPool(dbctx, cfg.DatabaseURL)
	c()
	if e != nil {
		return errors.New("database connection failed (check configuration and migration)")
	}
	defer db.Close()
	// No implicit migrations at server startup.
	var version int64
	if e = db.QueryRow(ctx, `SELECT max(version_id) FROM goose_db_version WHERE is_applied`).Scan(&version); e != nil || version < 1 {
		return errors.New("database migrations have not been applied")
	}
	app, e := gateway.New(context.Background(), cfg, db, nil, slog.Default())
	if e != nil {
		return errors.New("Gateway initialization failed")
	}
	defer app.Close()
	server := &http.Server{Addr: cfg.Listen, Handler: app.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	done := make(chan error, 1)
	go func() {
		slog.Info("喵连 Gateway listening", "address", cfg.Listen, "authMode", cfg.AuthMode)
		done <- server.ListenAndServe()
	}()
	select {
	case e := <-done:
		if errors.Is(e, http.ErrServerClosed) {
			return nil
		}
		return e
	case <-ctx.Done():
		app.Close()
		shutdown, c := context.WithTimeout(context.Background(), 20*time.Second)
		defer c()
		return server.Shutdown(shutdown)
	}
}
