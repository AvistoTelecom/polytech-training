package main

import "testing"

func TestScoresFromCounts(t *testing.T) {
	t.Parallel()

	got := scoresFromCounts(map[string]int64{"a": 3, "b": 1, "x": 9})
	if want := (scores{A: 3, B: 1}); got != want {
		t.Fatalf("scoresFromCounts() = %+v, want %+v", got, want)
	}

	if got := scoresFromCounts(nil); got != (scores{}) {
		t.Fatalf("scoresFromCounts(nil) = %+v, want zero", got)
	}
}

func TestBroadcasterReplaysLastEvent(t *testing.T) {
	t.Parallel()

	b := newBroadcaster()
	b.publish(event{name: "scores", data: `{"a":1,"b":0}`})

	ch := b.subscribe()
	defer b.unsubscribe(ch)

	if got := <-ch; got.data != `{"a":1,"b":0}` {
		t.Fatalf("subscriber got %+v, want last published event", got)
	}
}
