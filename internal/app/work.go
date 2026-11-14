package app

import (
	"context"

	"github.com/keyute/kopi/internal/event"
)

type Work struct {
	id     string
	cancel context.CancelFunc
	done   chan struct{}
	// outcome is written before done is closed.
	outcome event.Outcome
}

func (w *Work) ID() string { return w.id }

func (w *Work) Done() <-chan struct{} { return w.done }

// Outcome is valid once Done is closed.
func (w *Work) Outcome() event.Outcome { return w.outcome }
