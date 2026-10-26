package main

import (
	"regexp"
	"testing"
)

func TestValidVote(t *testing.T) {
	t.Parallel()

	for vote, want := range map[string]bool{"a": true, "b": true, "": false, "c": false, "A": false} {
		if got := validVote(vote); got != want {
			t.Errorf("validVote(%q) = %v, want %v", vote, got, want)
		}
	}
}

func TestNewVoterID(t *testing.T) {
	t.Parallel()

	id := newVoterID()
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(id) {
		t.Fatalf("newVoterID() = %q, want 16 hex chars", id)
	}

	if id == newVoterID() {
		t.Fatal("newVoterID() returned the same id twice")
	}
}
