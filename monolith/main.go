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

const pollInterval = time.Second

var errNoDatabase = errors.New("no database: POSTGRES_URL is empty or invalid")

type page struct {
	OptionA  string
	OptionB  string
	Hostname string
	Vote     string
	Logs     bool
}

type server struct {
	db       *pgxpool.Pool
	tmpl     *template.Template
	optionA  string
	optionB  string
	hostname string
	logs     bool
}

type scores struct {
	A int64 `json:"a"`
	B int64 `json:"b"`
}

func main() {
	hub := setupLogging()

	if err := run(hub); err != nil {
		slog.Error("monolith stopped", "error", err)
		os.Exit(1)
	}
}

func run(hub *logHub) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Never block the startup on the database: the pages stay reachable and the logs explain what is wrong.
	db := newPool(os.Getenv("POSTGRES_URL"))
	if db != nil {
		defer db.Close()

		go prepareDB(ctx, db)
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
		logs:     hub != nil,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /vote/{$}", s.handleVote)
	mux.HandleFunc("POST /vote/{$}", s.handleVote)
	mux.Handle("GET /vote/style.css", http.StripPrefix("/vote/", http.FileServerFS(voteFiles)))
	mux.HandleFunc("GET /result/{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		if err := resultTmpl.Execute(w, page{OptionA: s.optionA, OptionB: s.optionB, Logs: s.logs}); err != nil {
			slog.Error("Error rendering result page", "error", err)
		}
	})
	mux.Handle("GET /result/", http.StripPrefix("/result/", http.FileServerFS(resultFiles)))
	mux.HandleFunc("GET /result/scores", s.handleScores)

	if hub != nil {
		logFiles, err := fs.Sub(assets, "web/logs")
		if err != nil {
			return err
		}

		mux.Handle("GET /logs/stream", hub)
		mux.Handle("GET /logs/", http.StripPrefix("/logs/", http.FileServerFS(logFiles)))
	}

	return serve(ctx, ":"+getenv("PORT", "8080"), mux)
}

// newPool returns nil, after logging why, when the URL cannot be used. The pool connects lazily,
// so an unreachable database only shows up as query errors.
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

// prepareDB creates the votes table, retrying until the database answers.
func prepareDB(ctx context.Context, db *pgxpool.Pool) {
	delay := time.Second

	for {
		_, err := db.Exec(ctx, "CREATE TABLE IF NOT EXISTS votes (id VARCHAR(255) NOT NULL UNIQUE, vote VARCHAR(255) NOT NULL)")
		if err == nil {
			slog.Info("Connected to db")
			return
		}

		slog.Warn("Waiting for db", "error", err, "retry_in", delay)

		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}

		delay = min(delay*2, 10*time.Second)
	}
}

func (s *server) handleVote(w http.ResponseWriter, r *http.Request) {
	voterID := voterIDFromCookie(r)
	if voterID == "" {
		voterID = newVoterID()
	}

	data := page{OptionA: s.optionA, OptionB: s.optionB, Hostname: s.hostname, Logs: s.logs}

	if r.Method == http.MethodPost {
		vote := r.PostFormValue("vote")
		if !validVote(vote) {
			http.Error(w, "invalid vote", http.StatusBadRequest)
			return
		}

		if s.db == nil {
			slog.Error("Vote refused, no database", "error", errNoDatabase)
			http.Error(w, errNoDatabase.Error()+", see the logs", http.StatusServiceUnavailable)

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

	var lastErr string // only log when the error changes, not every second

	for {
		name, data := "scores", "{}"

		sc, err := queryScores(r.Context(), s.db)
		if err != nil {
			if err.Error() != lastErr {
				slog.Error("Error performing query", "error", err)
			}

			lastErr = err.Error()
			name = "dberror"
		} else if b, err := json.Marshal(sc); err == nil {
			data = string(b)
			lastErr = ""
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
