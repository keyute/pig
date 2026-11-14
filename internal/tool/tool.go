package tool

import (
	"context"
	"encoding/json"

	"github.com/keyute/kopi/internal/policy"
)

type Tool struct {
	Name, Label, Description string
	// Parameters is the JSON schema object for the arguments.
	Parameters map[string]any
	Capability policy.Capability
	Run        func(ctx context.Context, args json.RawMessage) (string, error)
}
