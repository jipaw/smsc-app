// Package bus: event bus in-process + ring buffer untuk SSE (replay 5 menit, ADR-08).
package bus

import (
	"encoding/json"
	"sync"
	"time"
)

type Event struct {
	ID   int64
	Name string
	Data []byte
	At   time.Time
}

type Bus struct {
	mu     sync.Mutex
	epoch  int64
	next   int64
	ring   []Event
	subs   map[chan Event]struct{}
	replay time.Duration
}

func New(replay time.Duration) *Bus {
	return &Bus{epoch: time.Now().UnixNano(), replay: replay, subs: map[chan Event]struct{}{}, next: 1}
}

// Epoch membedakan proses; Last-Event-ID dari epoch lain harus memicu resync.
func (b *Bus) Epoch() int64 { return b.epoch }

func (b *Bus) Publish(name string, data any) {
	raw, err := json.Marshal(data)
	if err != nil {
		return
	}
	b.mu.Lock()
	ev := Event{ID: b.next, Name: name, Data: raw, At: time.Now()}
	b.next++
	b.ring = append(b.ring, ev)
	cut := time.Now().Add(-b.replay)
	for len(b.ring) > 0 && (b.ring[0].At.Before(cut) || len(b.ring) > 5000) {
		b.ring = b.ring[1:]
	}
	for ch := range b.subs {
		select {
		case ch <- ev:
		default: // pelanggan lambat: lewati (klien akan resync)
		}
	}
	b.mu.Unlock()
}

// Subscribe mengembalikan kanal, event replay sejak lastID (ok=false bila lastID terlalu lama -> klien harus resync), dan fungsi berhenti.
func (b *Bus) Subscribe(lastID int64, hasLast bool) (<-chan Event, []Event, bool, func()) {
	ch := make(chan Event, 256)
	b.mu.Lock()
	defer b.mu.Unlock()
	ok := true
	var replay []Event
	if hasLast {
		if len(b.ring) == 0 {
			ok = lastID == b.next-1
		} else if lastID < b.ring[0].ID-1 {
			ok = false
		} else {
			for _, e := range b.ring {
				if e.ID > lastID {
					replay = append(replay, e)
				}
			}
		}
	}
	b.subs[ch] = struct{}{}
	return ch, replay, ok, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}
}
