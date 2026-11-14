package app

import "errors"

var (
	ErrQuit             = errors.New("quit requested")
	ErrBusy             = errors.New("a turn is already running")
	ErrUnknownCommand   = errors.New("unknown command")
	ErrNoSession        = errors.New("no open session")
	ErrWorkStillRunning = errors.New("work is still running")
)
