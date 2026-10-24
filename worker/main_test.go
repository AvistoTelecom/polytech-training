package main

import "testing"

func TestDecodeBallot(t *testing.T) {
	tests := []struct {
		payload string
		want    ballot
		wantErr bool
	}{
		{payload: `{"voter_id":"abc","vote":"a"}`, want: ballot{VoterID: "abc", Vote: "a"}},
		{payload: `{"voter_id":"abc"}`, wantErr: true},
		{payload: `not json`, wantErr: true},
	}
	for _, tt := range tests {
		got, err := decodeBallot(tt.payload)
		if (err != nil) != tt.wantErr {
			t.Errorf("decodeBallot(%q) error = %v, wantErr %v", tt.payload, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("decodeBallot(%q) = %+v, want %+v", tt.payload, got, tt.want)
		}
	}
}
