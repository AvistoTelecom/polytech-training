package main

import (
	"fmt"
	"net/http"
	"sync"
)

type event struct {
	name string
	data string
}

// broadcaster fans out the latest event to every connected SSE client.
type broadcaster struct {
	mu      sync.Mutex
	clients map[chan event]struct{}
	last    *event
}

func newBroadcaster() *broadcaster {
	return &broadcaster{clients: map[chan event]struct{}{}}
}

func (b *broadcaster) publish(e event) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.last = &e
	for ch := range b.clients {
		select {
		case ch <- e:
		default: // slow client: drop this update, the next one follows in a second
		}
	}
}

func (b *broadcaster) subscribe() chan event {
	ch := make(chan event, 1)

	b.mu.Lock()
	defer b.mu.Unlock()

	b.clients[ch] = struct{}{}
	if b.last != nil {
		ch <- *b.last
	}
	return ch
}

func (b *broadcaster) unsubscribe(ch chan event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.clients, ch)
}

// ServeHTTP streams events to the client until it disconnects.
func (b *broadcaster) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := b.subscribe()
	defer b.unsubscribe(ch)

	for {
		select {
		case <-r.Context().Done():
			return
		case e := <-ch:
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.name, e.data); err != nil {
				return
			}
			if err := rc.Flush(); err != nil {
				return
			}
		}
	}
}
