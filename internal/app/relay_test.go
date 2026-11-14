package app

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/keyute/kopi/internal/event"
)

func collect(r *relay) (*[]event.Event, func()) {
	var got []event.Event
	unsubscribe := r.subscribe(func(ev event.Event) { got = append(got, ev) })
	return &got, unsubscribe
}

func TestRelayDeliversCurrentEpochToAllSubscribers(t *testing.T) {
	var r relay
	a, _ := collect(&r)
	b, _ := collect(&r)

	r.bind()(event.TextDelta{Text: "bound"})
	r.emit(event.Notice{Text: "controller"})

	want := []event.Event{event.TextDelta{Text: "bound"}, event.Notice{Text: "controller"}}
	assert.Equal(t, want, *a)
	assert.Equal(t, want, *b)
}

func TestRelayDropsStaleEpochAfterAdvance(t *testing.T) {
	var r relay
	got, _ := collect(&r)
	old := r.bind()
	r.advance()
	current := r.bind()

	old(event.TextDelta{Text: "late"})
	current(event.TextDelta{Text: "fresh"})
	r.emit(event.Notice{Text: "controller"})

	assert.Equal(t, []event.Event{event.TextDelta{Text: "fresh"}, event.Notice{Text: "controller"}}, *got)
}

func TestRelayUnsubscribeStopsDelivery(t *testing.T) {
	var r relay
	got, unsubscribe := collect(&r)
	kept, _ := collect(&r)
	r.emit(event.Notice{Text: "one"})
	unsubscribe()
	r.emit(event.Notice{Text: "two"})

	assert.Equal(t, []event.Event{event.Notice{Text: "one"}}, *got)
	assert.Equal(t, []event.Event{event.Notice{Text: "one"}, event.Notice{Text: "two"}}, *kept)
}

func TestRelayShutStopsAllDelivery(t *testing.T) {
	var r relay
	got, _ := collect(&r)
	bound := r.bind()
	r.shut()

	bound(event.TextDelta{Text: "late"})
	r.emit(event.Notice{Text: "controller"})

	assert.Empty(t, *got)
}
