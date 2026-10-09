package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"github.com/A11myt/tuipod/internal/auth"
	"github.com/A11myt/tuipod/internal/db"
	"github.com/A11myt/tuipod/internal/podcast"
	"github.com/A11myt/tuipod/internal/push"
)

// main runs the podcast feed refresher as a standalone background process,
// separate from the HTTP API (cmd/server). Only ever run ONE replica of this
// process — refreshOne notifies subscribers on new episodes, so running
// multiple instances would send duplicate push notifications. The API can
// still be scaled to N instances freely since it no longer runs this loop.
// Assumes the DB schema is already migrated (cmd/server owns migrations).
func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	if err := godotenv.Load(); err != nil {
		slog.Info("no .env file, reading from environment")
	}

	pool, err := db.New(context.Background())
	if err != nil {
		slog.Error("db init failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	refreshHours := 6
	if v := os.Getenv("REFRESH_INTERVAL_HOURS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			refreshHours = n
		}
	}

	notifier := push.NewNotifier()
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		slog.Info("worker running", "refresh_interval_hours", refreshHours)
		podcast.NewRefresher(pool, notifier).Run(ctx, time.Duration(refreshHours)*time.Hour)
	}()

	// Expired refresh/password-reset/email-verification tokens are never
	// deleted anywhere else — see auth.RunTokenCleanup's doc comment. Once
	// a day is plenty; this isn't time-sensitive the way feed refreshing is.
	go auth.RunTokenCleanup(ctx, pool, 24*time.Hour)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)
	<-quit

	cancel()
	slog.Info("worker stopped")
}
