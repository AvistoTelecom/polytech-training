// Command worker moves votes from the Valkey list "votes" into the Postgres table "votes".
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/valkey-io/valkey-go"
)

const (
	votesKey    = "votes"
	popTimeout  = 5 * time.Second
	healthzAddr = ":8080"
	warmUp      = 30 * time.Second

	upsertVote = `INSERT INTO votes (id, vote) VALUES ($1, $2)
		ON CONFLICT (id) DO UPDATE SET vote = EXCLUDED.vote`
)

type ballot struct {
	VoterID string `json:"voter_id"`
	Vote    string `json:"vote"`
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	// A cancelled context means a graceful shutdown (SIGINT/SIGTERM), not a failure.
	if err := run(); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("worker stopped", "error", err)
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

	valkeyURL := os.Getenv("VALKEY_URL")
	if valkeyURL == "" {
		return errors.New("VALKEY_URL env var is empty")
	}

	// Feature flag: prepare the cache before reporting healthy. The warm-up is simulated (the
	// worker only waits), which makes a rolling update easy to watch.
	if enabled("FEATURE_CACHE_WARMUP") {
		slog.Info("Warming up the cache", "duration", warmUp)

		if !sleep(ctx, warmUp) {
			return ctx.Err()
		}

		slog.Info("Cache ready")
	}

	db, err := connectPostgres(ctx, postgresURL)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := ensureSchema(ctx, db); err != nil {
		return err
	}

	vk, err := connectValkey(ctx, valkeyURL)
	if err != nil {
		return err
	}
	defer vk.Close()

	go serveHealthz(ctx)

	// Feature flag (experimental): store the votes in batches of BATCH_SIZE (100 by default).
	if enabled("FEATURE_BATCH_INSERT") {
		size, _ := strconv.Atoi(os.Getenv("BATCH_SIZE"))
		return processVotesInBatches(ctx, vk, db, size)
	}

	return processVotes(ctx, vk, db)
}

// enabled reports whether the feature flag in env var name is on ("true", "1"…).
func enabled(name string) bool {
	on, _ := strconv.ParseBool(os.Getenv(name))
	return on
}

// processVotes blocks on the Valkey list and stores each vote until ctx is cancelled.
func processVotes(ctx context.Context, vk valkey.Client, db *pgxpool.Pool) error {
	for {
		cmd := vk.B().Blpop().Key(votesKey).Timeout(popTimeout.Seconds()).Build()

		res, err := vk.Do(ctx, cmd).AsStrSlice()
		switch {
		case ctx.Err() != nil:
			return ctx.Err()
		case valkey.IsValkeyNil(err):
			continue // timeout, nothing to process
		case err != nil:
			slog.Error("Waiting for valkey", "error", err)
			sleep(ctx, time.Second)

			continue
		}

		// BLPOP returns [key, value].
		b, err := decodeBallot(res[1])
		if err != nil {
			slog.Error("Skipping invalid vote", "payload", res[1], "error", err)
			continue
		}

		slog.Info("Processing vote", "vote", b.Vote, "voter_id", b.VoterID)

		if err := storeVote(ctx, db, b); err != nil {
			slog.Error("Could not store vote", "error", err)
		}
	}
}

// processVotesInBatches stores the votes by batches of size, in one round trip each, so the
// database gets fewer and larger writes under heavy load. A partial batch is stored as soon as
// the Valkey list stays empty for a pop window.
func processVotesInBatches(ctx context.Context, vk valkey.Client, db *pgxpool.Pool, size int) error {
	// Share the pop timeout across the batch, so a full batch never waits longer than one pop.
	popWindow := popTimeout / time.Duration(size)
	batch := make([]ballot, 0, size)

	slog.Info("Batch insert enabled", "size", size, "pop_window", popWindow)

	for {
		cmd := vk.B().Blpop().Key(votesKey).Timeout(popWindow.Seconds()).Build()

		res, err := vk.Do(ctx, cmd).AsStrSlice()
		switch {
		case ctx.Err() != nil:
			return ctx.Err()
		case valkey.IsValkeyNil(err):
			batch = flushVotes(ctx, db, batch)
			continue
		case err != nil:
			slog.Error("Waiting for valkey", "error", err)
			sleep(ctx, time.Second)

			continue
		}

		b, err := decodeBallot(res[1])
		if err != nil {
			slog.Error("Skipping invalid vote", "payload", res[1], "error", err)
			continue
		}

		batch = append(batch, b)
		if len(batch) >= size {
			batch = flushVotes(ctx, db, batch)
		}
	}
}

// flushVotes stores the batch, if any, and returns it emptied.
func flushVotes(ctx context.Context, db *pgxpool.Pool, batch []ballot) []ballot {
	if len(batch) == 0 {
		return batch
	}

	if err := storeVotes(ctx, db, batch); err != nil {
		slog.Error("Could not store votes", "count", len(batch), "error", err)
	} else {
		slog.Info("Processed votes", "count", len(batch))
	}

	return batch[:0]
}

func decodeBallot(payload string) (ballot, error) {
	var b ballot
	if err := json.Unmarshal([]byte(payload), &b); err != nil {
		return b, err
	}

	if b.VoterID == "" || b.Vote == "" {
		return b, errors.New("missing voter_id or vote")
	}

	return b, nil
}

// storeVote keeps one vote per voter: a new vote from the same voter replaces the previous one.
func storeVote(ctx context.Context, db *pgxpool.Pool, b ballot) error {
	_, err := db.Exec(ctx, upsertVote, b.VoterID, b.Vote)

	return err
}

// storeVotes stores several votes in one round trip, with the same rule as storeVote.
func storeVotes(ctx context.Context, db *pgxpool.Pool, votes []ballot) error {
	batch := &pgx.Batch{}
	for _, b := range votes {
		batch.Queue(upsertVote, b.VoterID, b.Vote)
	}

	return db.SendBatch(ctx, batch).Close()
}

func ensureSchema(ctx context.Context, db *pgxpool.Pool) error {
	_, err := db.Exec(ctx, `CREATE TABLE IF NOT EXISTS votes (
		id VARCHAR(255) NOT NULL UNIQUE,
		vote VARCHAR(255) NOT NULL
	)`)
	if err != nil {
		return fmt.Errorf("create votes table: %w", err)
	}

	return nil
}

func connectPostgres(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("parse POSTGRES_URL: %w", err)
	}

	for {
		err := pool.Ping(ctx)
		if err == nil {
			slog.Info("Connected to db")
			return pool, nil
		}

		slog.Warn("Waiting for db", "error", err)

		if !sleep(ctx, time.Second) {
			pool.Close()
			return nil, ctx.Err()
		}
	}
}

func connectValkey(ctx context.Context, url string) (valkey.Client, error) {
	opts, err := valkey.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parse VALKEY_URL: %w", err)
	}

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

		slog.Warn("Waiting for valkey", "error", err)

		if !sleep(ctx, time.Second) {
			return nil, ctx.Err()
		}
	}
}

// serveHealthz exposes the liveness endpoint used by the Kubernetes probes.
func serveHealthz(ctx context.Context) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("Healthy"))
	})
	srv := &http.Server{Addr: healthzAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	go func() {
		<-ctx.Done()

		_ = srv.Close()
	}()

	slog.Info("Health check server started", "addr", healthzAddr+"/healthz")

	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("Health check server failed", "error", err)
	}
}

// sleep waits for d, returning false if ctx was cancelled first.
func sleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
