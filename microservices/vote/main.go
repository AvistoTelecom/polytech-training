// Command vote serves the voting page and pushes each vote to the Valkey list "votes".
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
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/valkey-io/valkey-go"
)

//go:embed templates static
var assets embed.FS

const votesKey = "votes"

type ballot struct {
	VoterID string `json:"voter_id"`
	Vote    string `json:"vote"`
}

type page struct {
	OptionA  string
	OptionB  string
	Hostname string
	Vote     string
	Logs     bool
}

type server struct {
	valkey   atomic.Pointer[valkey.Client] // nil until the background connection succeeds
	tmpl     *template.Template
	optionA  string
	optionB  string
	hostname string
	logs     bool
}

func main() {
	hub := setupLogging()

	if err := run(hub); err != nil {
		slog.Error("vote stopped", "error", err)
		os.Exit(1)
	}
}

func run(hub *logHub) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	tmpl, err := template.ParseFS(assets, "templates/index.html")
	if err != nil {
		return fmt.Errorf("parse template: %w", err)
	}

	hostname, _ := os.Hostname()
	s := &server{
		tmpl:     tmpl,
		optionA:  getenv("OPTION_A", "Cats"),
		optionB:  getenv("OPTION_B", "Dogs"),
		hostname: hostname,
		logs:     hub != nil,
	}

	// Never block the startup on Valkey: the page and the logs must be reachable to debug a bad setup.
	go s.connect(ctx, os.Getenv("VALKEY_URL"))

	defer func() {
		if client := s.valkey.Load(); client != nil {
			(*client).Close()
		}
	}()

	static, err := fs.Sub(assets, "static")
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("POST /{$}", s.handleIndex)

	if hub != nil {
		mux.Handle("GET /logs", hub)
	}

	return serve(ctx, ":"+getenv("PORT", "8080"), mux)
}

// connect connects to Valkey in the background and logs why it cannot when the setup is wrong.
func (s *server) connect(ctx context.Context, url string) {
	if url == "" {
		slog.Error("VALKEY_URL env var is empty, votes cannot be recorded", "example", "valkey://valkey:6379/0")
		return
	}

	client, err := connectValkey(ctx, url)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			slog.Error("Giving up on valkey, votes cannot be recorded", "error", err)
		}

		return
	}

	s.valkey.Store(&client)
}

// connectValkey retries with exponential backoff until Valkey answers a PING.
func connectValkey(ctx context.Context, url string) (valkey.Client, error) {
	opts, err := valkey.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parse VALKEY_URL: %w", err)
	}

	delay := time.Second

	for {
		client, err := valkey.NewClient(opts)
		if err == nil {
			err = client.Do(ctx, client.B().Ping().Build()).Error()
			if err == nil {
				slog.Info("Connected to valkey")
				return client, nil
			}

			client.Close()
		}

		slog.Warn("Waiting for valkey", "error", err, "retry_in", delay)

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}

		delay = min(delay*2, 30*time.Second)
	}
}

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
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

		client := s.valkey.Load()
		if client == nil {
			slog.Error("Vote refused, valkey is not connected")
			http.Error(w, "valkey is not connected yet, see the logs", http.StatusServiceUnavailable)

			return
		}

		payload, err := json.Marshal(ballot{VoterID: voterID, Vote: vote})
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		cmd := (*client).B().Rpush().Key(votesKey).Element(string(payload)).Build()
		if err := (*client).Do(r.Context(), cmd).Error(); err != nil {
			slog.Error("push vote", "error", err)
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
	srv := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 5 * time.Second}

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
