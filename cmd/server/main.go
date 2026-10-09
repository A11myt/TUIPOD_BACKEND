package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/joho/godotenv"

	"github.com/A11myt/tuipod/docs"
	"github.com/A11myt/tuipod/internal/admin"
	"github.com/A11myt/tuipod/internal/auth"
	"github.com/A11myt/tuipod/internal/billing"
	"github.com/A11myt/tuipod/internal/bookmark"
	"github.com/A11myt/tuipod/internal/db"
	"github.com/A11myt/tuipod/internal/episode"
	"github.com/A11myt/tuipod/internal/favorite"
	"github.com/A11myt/tuipod/internal/mailer"
	"github.com/A11myt/tuipod/internal/metrics"
	"github.com/A11myt/tuipod/internal/podcast"
	"github.com/A11myt/tuipod/internal/push"
	"github.com/A11myt/tuipod/internal/queue"
	"github.com/A11myt/tuipod/internal/ratelimit"
	"github.com/A11myt/tuipod/internal/subscription"
	"github.com/A11myt/tuipod/internal/user"
)

// envInt reads an int env var, falling back to def if unset, unparseable,
// or <= 0 (guards against accidental zero/negative overrides, e.g. rate limits).
func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// main wires up the DB, HTTP routes/middleware, and starts the API server
// with graceful shutdown on SIGINT/SIGTERM. It only serves HTTP — the podcast
// feed refresher runs as a separate process (cmd/worker) so the API can be
// scaled to multiple instances without duplicating refresh cycles/push notifications.
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

	if err := db.RunMigrations(context.Background(), pool); err != nil {
		slog.Error("migrations failed", "err", err)
		os.Exit(1)
	}

	metrics.RegisterDBStats(pool)

	m := mailer.New()
	authH := auth.NewHandler(pool, m)
	userH := user.NewHandler(pool)
	podcastH := podcast.NewHandler(pool)
	subH := subscription.NewHandler(pool)
	episodeH := episode.NewHandler(pool)
	queueH := queue.NewHandler(pool)
	adminH := admin.NewHandler(pool)
	billingH := billing.NewHandler(pool)
	pushH := push.NewHandler(pool)
	favoriteH := favorite.NewHandler(pool)
	bookmarkH := bookmark.NewHandler(pool)

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(metrics.Middleware)

	// Caps every request body at 5 MiB — no handler here ever needs more
	// (the largest legitimate body is an OPML import with thousands of
	// subscriptions, still well under this), and without it a request of
	// unbounded size to any POST/PUT route — including unauthenticated ones
	// like /auth/register — could pressure memory. http.MaxBytesReader
	// makes the body reader itself return an error past the limit, so a
	// handler's normal json.NewDecoder(r.Body).Decode(...) call already
	// fails cleanly instead of needing a per-handler change.
	const maxBodyBytes = 5 << 20
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
			next.ServeHTTP(w, r)
		})
	})

	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	})

	r.Get("/metrics", func(w http.ResponseWriter, r *http.Request) {
		if token := os.Getenv("METRICS_TOKEN"); token != "" {
			if r.Header.Get("Authorization") != "Bearer "+token {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
		}
		metrics.Handler().ServeHTTP(w, r)
	})

	// Stripe webhooks — no auth middleware, raw body needed
	r.Post("/webhooks/stripe", billingH.Webhook)

	r.Get("/openapi.yaml", docs.SpecHandler())
	r.Get("/docs", docs.UIHandler())

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		if err := pool.Ping(r.Context()); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"status":"degraded"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})

	authLimit   := ratelimit.New(envInt("RATE_LIMIT_AUTH", 10), time.Minute)
	fetchLimit  := ratelimit.New(envInt("RATE_LIMIT_FETCH", 5), time.Minute)
	searchLimit := ratelimit.New(envInt("RATE_LIMIT_SEARCH", 20), time.Minute)
	importLimit := ratelimit.New(envInt("RATE_LIMIT_IMPORT", 2), time.Minute)
	globalLimit := ratelimit.New(envInt("RATE_LIMIT_GLOBAL", 100), time.Minute)

	r.Route("/v1", func(r chi.Router) {
		r.Use(globalLimit)

		// Public — stricter auth limit on top of global
		r.Group(func(r chi.Router) {
			r.Use(authLimit)
			r.Post("/auth/register", authH.Register)
			r.Post("/auth/login", authH.Login)
			r.Post("/auth/refresh", authH.Refresh)
			r.Post("/auth/logout", authH.Logout)
			r.Post("/auth/forgot-password", authH.ForgotPassword)
			r.Post("/auth/reset-password", authH.ResetPassword)
			r.Get("/auth/verify-email", authH.VerifyEmail)
		})

		// Protected — free for any logged-in user, regardless of plan
		r.Group(func(r chi.Router) {
			r.Use(auth.Middleware)

			r.Get("/me", userH.Me)
			r.Put("/me/password", userH.ChangePassword)
			r.Delete("/me", userH.Delete)

			r.Post("/me/device-tokens", pushH.Register)
			r.Delete("/me/device-tokens/{token}", pushH.Unregister)

			r.With(searchLimit).Get("/podcasts/search", podcastH.Search)
			r.With(fetchLimit).Post("/podcasts/fetch", podcastH.Fetch)
			r.Get("/podcasts/{id}", podcastH.GetPodcast)
			r.With(fetchLimit).Post("/podcasts/{id}/refresh", podcastH.Refresh)
			r.Get("/podcasts/{id}/episodes", podcastH.GetEpisodes)

			r.Get("/episodes/search", episodeH.Search)
			r.Get("/episodes/{id}", episodeH.GetEpisode)

			// data export always stays free — no lock-in on your own data
			r.Get("/subscriptions/export", subH.ExportOPML)

			r.Get("/billing/status", billingH.Status)
			r.Post("/billing/checkout", billingH.Checkout)
			r.Post("/billing/portal", billingH.Portal)

			// Sync — requires plan basic or pro. This is the paid feature: everything
			// here works fully locally/offline for free, syncing it across devices costs.
			r.Group(func(r chi.Router) {
				r.Use(billing.RequirePlan(pool, "basic", "pro"))

				r.Get("/me/feed", userH.Feed)
				r.Get("/me/continue-listening", userH.ContinueListening)
				r.Get("/me/history", userH.History)

				r.Get("/me/queue", queueH.List)
				r.Post("/me/queue", queueH.Add)
				r.Put("/me/queue/reorder", queueH.Reorder)
				r.Delete("/me/queue/{id}", queueH.Remove)

				r.Get("/me/favorites", favoriteH.List)
				r.Post("/me/favorites", favoriteH.Add)
				r.Delete("/me/favorites/{id}", favoriteH.Remove)

				r.Get("/me/bookmarks", bookmarkH.List)
				r.Post("/me/bookmarks", bookmarkH.Create)
				r.Delete("/me/bookmarks/{id}", bookmarkH.Delete)

				r.Get("/subscriptions", subH.List)
				r.Post("/subscriptions", subH.Subscribe)
				r.Delete("/subscriptions/{id}", subH.Unsubscribe)

				r.Get("/episodes/{id}/progress", episodeH.GetProgress)
				r.Put("/episodes/{id}/progress", episodeH.UpsertProgress)
			})

			// Pro-only
			r.Group(func(r chi.Router) {
				r.Use(billing.RequirePlan(pool, "pro"))

				r.Get("/me/stats", userH.Stats)
				r.With(importLimit).Post("/subscriptions/import", subH.ImportOPML)
			})

			// Admin — requires is_admin = true (independent of plan)
			r.Group(func(r chi.Router) {
				r.Use(admin.Middleware(pool))
				r.Get("/admin/stats", adminH.Stats)
				r.Get("/admin/stats/growth", adminH.GrowthStats)
				r.Get("/admin/users", adminH.ListUsers)
				r.Get("/admin/users/{id}", adminH.GetUser)
				r.Put("/admin/users/{id}/plan", adminH.SetPlan)
				r.Post("/admin/users/{id}/suspend", adminH.Suspend)
				r.Post("/admin/users/{id}/unsuspend", adminH.Unsuspend)
				r.Delete("/admin/users/{id}", adminH.DeleteUser)
			})
		})
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	srv := &http.Server{
		Addr:    ":" + port,
		Handler: r,
	}

	go func() {
		slog.Info("server running", "port", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)
	<-quit

	slog.Info("shutting down...")
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutCancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		slog.Error("shutdown error", "err", err)
		os.Exit(1)
	}
	slog.Info("server stopped")
}
