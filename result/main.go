// Command result serves a page showing live vote results, streamed over Server-Sent Events.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
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

const (
	pollInterval    = time.Second
	connectAttempts = 3
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	if err := run(); err != nil {
		slog.Error("result stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	postgresURL := os.Getenv("POSTGRES_URL")
	if postgresURL == "" {
		return errors.New("POSTGRES_URL env var is empty")
	}

	// Start the server only after a successful DB connection.
	db, err := connectPostgres(ctx, postgresURL)
	if err != nil {
		return err
	}
	defer db.Close()

	b := newBroadcaster()
	go pollScores(ctx, db, b)

	views, err := fs.Sub(assets, "views")
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(views))
	mux.Handle("GET /scores", b)

	return serve(ctx, ":"+getenv("PORT", "4000"), mux)
}

func connectPostgres(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("parse POSTGRES_URL: %w", err)
	}
	for attempt := 1; ; attempt++ {
		err = pool.Ping(ctx)
		if err == nil {
			slog.Info("Connected to db")
			return pool, nil
		}
		slog.Warn("Waiting for db", "error", err)
		if attempt == connectAttempts {
			pool.Close()
			return nil, fmt.Errorf("giving up, could not connect to database: %w", err)
		}
		select {
		case <-ctx.Done():
			pool.Close()
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// pollScores queries the vote counts every pollInterval and publishes them.
func pollScores(ctx context.Context, db *pgxpool.Pool, b *broadcaster) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		s, err := queryScores(ctx, db)
		if err != nil {
			slog.Error("Error performing query", "error", err)
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

type scores struct {
	A int64 `json:"a"`
	B int64 `json:"b"`
}

func queryScores(ctx context.Context, db *pgxpool.Pool) (scores, error) {
	rows, err := db.Query(ctx, "SELECT vote, COUNT(id) AS count FROM votes GROUP BY vote")
	if err != nil {
		return scores{}, err
	}
	defer rows.Close()

	counts := map[string]int64{}
	for rows.Next() {
		var vote string
		var count int64
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
