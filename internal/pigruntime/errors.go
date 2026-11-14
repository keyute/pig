package pigruntime

import "errors"

var (
	ErrUnknownModel    = errors.New("unknown model")
	ErrJoinTimeout     = errors.New("session run did not stop in time")
	ErrNoCredential    = errors.New("no credential")
	ErrUnknownProvider = errors.New("unknown provider")
	ErrNoOAuth         = errors.New("provider does not offer OAuth login")
	ErrSessionNotFound = errors.New("session not found")
)
