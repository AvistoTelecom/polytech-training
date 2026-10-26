// Command monolith serves the vote page (/vote) and the live results page (/result) from a
// single process that talks to Postgres only: no Valkey and no worker.
package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
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

//go:embed web
var assets embed.FS

const (
	pollInterval = time.Second
	// A new connection pool is lazy, so the database may still be starting: retry for a while.
	connectAttempts = 10
)

type page struct {
	OptionA  string
	OptionB  string
	Hostname string
	Vote     string
}

type server struct {
	db       *pgxpool.Pool
	tmpl     *template.Template
	optionA  string
	optionB  string
	hostname string
}

type scores struct {
	A int64 `json:"a"`
	B int64 `json:"b"`
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	if err := run(); err != nil {
		slog.Error("monolith stopped", "error", err)
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

	db, err := connectPostgres(ctx, postgresURL)
	if err != nil {
		return err
	}
	defer db.Close()

	if _, err := db.Exec(ctx, "CREATE TABLE IF NOT EXISTS votes (id VARCHAR(255) NOT NULL UNIQUE, vote VARCHAR(255) NOT NULL)"); err != nil {
		return fmt.Errorf("create votes table: %w", err)
	}

	tmpl, err := template.ParseFS(assets, "web/vote/index.html")
	if err != nil {
		return fmt.Errorf("parse template: %w", err)
	}

	voteFiles, err := fs.Sub(assets, "web/vote")
	if err != nil {
		return err
	}

	resultFiles, err := fs.Sub(assets, "web/result")
	if err != nil {
		return err
	}

	resultTmpl, err := template.ParseFS(assets, "web/result/index.html")
	if err != nil {
		return fmt.Errorf("parse template: %w", err)
	}

	hostname, _ := os.Hostname()
	s := &server{
		db:       db,
		tmpl:     tmpl,
		optionA:  getenv("OPTION_A", "Cats"),
		optionB:  getenv("OPTION_B", "Dogs"),
		hostname: hostname,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /vote/{$}", s.handleVote)
	mux.HandleFunc("POST /vote/{$}", s.handleVote)
	mux.Handle("GET /vote/style.css", http.StripPrefix("/vote/", http.FileServerFS(voteFiles)))
	mux.HandleFunc("GET /result/{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		if err := resultTmpl.Execute(w, page{OptionA: s.optionA, OptionB: s.optionB}); err != nil {
			slog.Error("Error rendering result page", "error", err)
		}
	})
	mux.Handle("GET /result/", http.StripPrefix("/result/", http.FileServerFS(resultFiles)))
	mux.HandleFunc("GET /result/scores", s.handleScores)

	return serve(ctx, ":"+getenv("PORT", "8080"), mux)
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

func (s *server) handleVote(w http.ResponseWriter, r *http.Request) {
	voterID := voterIDFromCookie(r)
	if voterID == "" {
		voterID = newVoterID()
	}

	data := page{OptionA: s.optionA, OptionB: s.optionB, Hostname: s.hostname}

	if r.Method == http.MethodPost {
		vote := r.PostFormValue("vote")
		if !validVote(vote) {
			http.Error(w, "invalid vote", http.StatusBadRequest)
			return
		}

		// One vote per voter: voting again replaces the previous choice.
		_, err := s.db.Exec(r.Context(),
			"INSERT INTO votes (id, vote) VALUES ($1, $2) ON CONFLICT (id) DO UPDATE SET vote = $2",
			voterID, vote)
		if err != nil {
			slog.Error("record vote", "error", err)
			http.Error(w, "could not record vote", http.StatusServiceUnavailable)

			return
		}

		slog.Info("Received vote", "vote", vote)
		data.Vote = vote
	}

	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: no Secure flag, the labs serve the app over plain HTTP
		Name:     "voter_id",
		Value:    voterID,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	if err := s.tmpl.Execute(w, data); err != nil {
		slog.Error("render page", "error", err)
	}
}

// handleScores streams the vote counts as Server-Sent Events, one query per client every pollInterval.
func (s *server) handleScores(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		name, data := "scores", "{}"

		sc, err := queryScores(r.Context(), s.db)
		if err != nil {
			slog.Error("Error performing query", "error", err)

			name = "dberror"
		} else if b, err := json.Marshal(sc); err == nil {
			data = string(b)
		}

		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, data); err != nil {
			return
		}

		flusher.Flush()

		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

func queryScores(ctx context.Context, db *pgxpool.Pool) (scores, error) {
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

	return scores{A: counts["a"], B: counts["b"]}, nil
}

func voterIDFromCookie(r *http.Request) string {
	c, err := r.Cookie("voter_id")
	if err != nil {
		return ""
	}

	return c.Value
}

func newVoterID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b) // crypto/rand.Read never returns an error

	return hex.EncodeToString(b)
}

func validVote(v string) bool {
	return v == "a" || v == "b"
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
		// Lets the long-lived /result/scores streams end on shutdown.
		BaseContext: func(_ net.Listener) context.Context { return ctx },
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
