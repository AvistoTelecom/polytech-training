package main

import "testing"

func TestNewPoolIgnoresWhitespace(t *testing.T) {
	t.Parallel()

	pool, err := newPool(" postgres://user:pass@db.example.com:5432/vote?\n\tsslmode=require&pool_max_conns=5\n")
	if err != nil {
		t.Fatalf("newPool: %v", err)
	}
	defer pool.Close()

	cfg := pool.Config()
	if cfg.MaxConns != 5 {
		t.Errorf("MaxConns = %d, want 5", cfg.MaxConns)
	}

	if cfg.ConnConfig.TLSConfig == nil {
		t.Error("TLSConfig is nil, want sslmode=require to enable TLS")
	}

	for k := range cfg.ConnConfig.RuntimeParams {
		t.Errorf("unexpected runtime param %q", k)
	}
}
