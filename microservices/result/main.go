// Command result serves a page showing live vote results, streamed over Server-Sent Events.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed views
var assets embed.FS

const pollInterval = time.Second

var errNoDatabase = errors.New("no database: POSTGRES_URL is empty or invalid")

func main() {
	hub := setupLogging()

	if err := run(hub); err != nil {
		slog.Error("result stopped", "error", err)
		os.Exit(1)
	}
}

func run(hub *logHub) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Never block the startup on the database: the page reports the error and the logs explain it.
	db := newPool(os.Getenv("POSTGRES_URL"))
	if db != nil {
		defer db.Close()
	}

	b := newBroadcaster()
	go pollScores(ctx, db, b)

	views, err := fs.Sub(assets, "views")
	if err != nil {
		return err
	}

	tmpl, err := template.ParseFS(assets, "views/index.html")
	if err != nil {
		return fmt.Errorf("parse template: %w", err)
	}

	page := options{OptionA: getenv("OPTION_A", "Cats"), OptionB: getenv("OPTION_B", "Dogs"), Logs: hub != nil}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		if err := tmpl.Execute(w, page); err != nil {
			slog.Error("Error rendering page", "error", err)
		}
	})
	mux.Handle("GET /", http.FileServerFS(views))
	mux.Handle("GET /scores", b)

	if hub != nil {
		mux.Handle("GET /logs", hub)
	}

	return serve(ctx, ":"+getenv("PORT", "4000"), mux)
}

// newPool returns nil, after logging why, when the URL cannot be used. The pool connects lazily,
// so an unreachable database only shows up as query errors, which pollScores reports.
func newPool(url string) *pgxpool.Pool {
	if url == "" {
		slog.Error("POSTGRES_URL env var is empty", "example", "postgres://user:pass@db:5432/postgres")
		return nil
	}

	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		slog.Error("Invalid POSTGRES_URL", "error", err)
		return nil
	}

	return pool
}

// pollScores queries the vote counts every pollInterval and publishes them.
func pollScores(ctx context.Context, db *pgxpool.Pool, b *broadcaster) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	// Only log when the outcome changes, not every second.
	var (
		lastErr   string
		connected bool
	)

	for {
		s, err := queryScores(ctx, db)

		switch {
		case err != nil && err.Error() != lastErr:
			slog.Error("Error performing query", "error", err)

			lastErr, connected = err.Error(), false
		case err == nil && !connected:
			slog.Info("Connected to db")

			lastErr, connected = "", true
		}

		if err != nil {
			b.publish(event{name: "dberror", data: "{}"})
		} else {
			data, _ := json.Marshal(s)
			b.publish(event{name: "scores", data: string(data)})
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// options holds the labels of the two choices, shown on the page.
type options struct {
	OptionA string
	OptionB string
	Logs    bool
}

type scores struct {
	A int64 `json:"a"`
	B int64 `json:"b"`
}

func queryScores(ctx context.Context, db *pgxpool.Pool) (scores, error) {
	if db == nil {
		return scores{}, errNoDatabase
	}

	rows, err := db.Query(ctx, "SELECT vote, COUNT(id) AS count FROM votes GROUP BY vote")
	if err != nil {
		return scores{}, err
	}
	defer rows.Close()

	counts := map[string]int64{}

	for rows.Next() {
		var (
			vote  string
			count int64
		)
		if err := rows.Scan(&vote, &count); err != nil {
			return scores{}, err
		}

		counts[vote] = count
	}

	if err := rows.Err(); err != nil {
		return scores{}, err
	}

	return scoresFromCounts(counts), nil
}

func scoresFromCounts(counts map[string]int64) scores {
	return scores{A: counts["a"], B: counts["b"]}
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return fallback
}

// serve runs the HTTP server until ctx is cancelled, then shuts it down gracefully.
func serve(ctx context.Context, addr string, h http.Handler) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		BaseContext:       func(_ net.Listener) context.Context { return ctx },
	}

	errCh := make(chan error, 1)

	go func() {
		slog.Info("App running", "addr", addr)

		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	return srv.Shutdown(shutdownCtx)
}
