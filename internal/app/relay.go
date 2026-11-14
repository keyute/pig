package app

import (
	"slices"
	"sync"

	"github.com/keyute/kopi/internal/event"
)

type subscriber struct {
	id int
	fn func(event.Event)
}

// relay fans events out to subscribers. Every emit carries the epoch it was
// bound at; delivery, advance and shut hold the same mutex, so once advance
// returns no event bound to an earlier epoch is delivered, and once shut
// returns nothing is delivered at all.
type relay struct {
	mu     sync.Mutex
	epoch  uint64
	nextID int
	subs   []subscriber
	closed bool
}

func (r *relay) subscribe(fn func(event.Event)) func() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	id := r.nextID
	r.subs = append(r.subs, subscriber{id: id, fn: fn})
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.subs = slices.DeleteFunc(r.subs, func(s subscriber) bool { return s.id == id })
	}
}

// bind returns an emit function stamped with the current epoch.
func (r *relay) bind() func(event.Event) {
	r.mu.Lock()
	epoch := r.epoch
	r.mu.Unlock()
	return func(ev event.Event) { r.deliver(epoch, ev) }
}

func (r *relay) advance() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.epoch++
}

func (r *relay) shut() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
}

func (r *relay) emit(ev event.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.send(ev)
}

// deliver runs subscribers under the mutex: that is what makes advance a
// barrier, and why subscribers must not block or call back into the relay.
func (r *relay) deliver(epoch uint64, ev event.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if epoch != r.epoch {
		return
	}
	r.send(ev)
}

func (r *relay) send(ev event.Event) {
	if r.closed {
		return
	}
	for _, s := range r.subs {
		s.fn(ev)
	}
}
