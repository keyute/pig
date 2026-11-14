package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParse(t *testing.T) {
	for _, tc := range []struct {
		line, name, args string
		isCommand        bool
	}{
		{line: "/plan", name: "plan", isCommand: true},
		{line: "/resume abc", name: "resume", args: "abc", isCommand: true},
		{line: "  /status  ", name: "status", isCommand: true},
		{line: "/frobnicate now", name: "frobnicate", args: "now", isCommand: true},
		{line: "//x"},
		{line: "explain this repo"},
		{line: ""},
	} {
		t.Run(tc.line, func(t *testing.T) {
			name, args, isCommand := parse(tc.line)
			assert.Equal(t, tc.name, name)
			assert.Equal(t, tc.args, args)
			assert.Equal(t, tc.isCommand, isCommand)
		})
	}
}
