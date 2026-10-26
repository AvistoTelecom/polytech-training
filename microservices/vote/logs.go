package main

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
)

const logHistory = 200

// logHub keeps the latest log lines in memory and streams them to browsers over SSE,
// so students can read the logs in the web UI without any tooling.
type logHub struct {
	mu      sync.Mutex
	lines   []string
	clients map[chan string]struct{}
}

// setupLogging sends the logs to stdout and, unless LOG_VIEWER is "false", to a logHub.
func setupLogging() *logHub {
	var hub *logHub

	out := io.Writer(os.Stdout)

	switch strings.ToLower(os.Getenv("LOG_VIEWER")) {
	case "false", "0", "off", "no":
	default:
		hub = &logHub{clients: map[chan string]struct{}{}}
		out = io.MultiWriter(os.Stdout, hub)
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(out, nil)))

	return hub
}

// Write receives one formatted log record per call (slog's text handler guarantees it).
func (h *logHub) Write(p []byte) (int, error) {
	line := strings.TrimRight(string(p), "\n")

	h.mu.Lock()
	defer h.mu.Unlock()

	h.lines = append(h.lines, line)
	if len(h.lines) > logHistory {
		h.lines = h.lines[1:]
	}

	for ch := range h.clients {
		select {
		case ch <- line:
		default: // slow client: drop the line rather than block the application
		}
	}

	return len(p), nil
}

func (h *logHub) subscribe() ([]string, chan string) {
	ch := make(chan string, logHistory)

	h.mu.Lock()
	defer h.mu.Unlock()

	h.clients[ch] = struct{}{}

	return append([]string(nil), h.lines...), ch
}

func (h *logHub) unsubscribe(ch chan string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	delete(h.clients, ch)
}

// ServeHTTP replays the recent history, then streams new lines until the client disconnects.
func (h *logHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")

	history, ch := h.subscribe()
	defer h.unsubscribe(ch)

	for _, line := range history {
		if _, err := fmt.Fprintf(w, "data: %s\n\n", line); err != nil {
			return
		}
	}

	if err := rc.Flush(); err != nil {
		return
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case line := <-ch:
			if _, err := fmt.Fprintf(w, "data: %s\n\n", line); err != nil {
				return
			}

			if err := rc.Flush(); err != nil {
				return
			}
		}
	}
}
