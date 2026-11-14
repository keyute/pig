package app

import (
	"fmt"
	"strings"

	"github.com/keyute/kopi/internal/tool"
)

func systemPrompt(workspace string, tools []tool.Tool) string {
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name)
	}
	available := "No tools are available."
	if len(names) > 0 {
		available = "Tools available: " + strings.Join(names, ", ") + "."
	}
	return strings.Join([]string{
		fmt.Sprintf("You are Kopi, a coding agent working in %s.", workspace),
		available,
		"The session has a plan mode and an execute mode; only the user can switch between them.",
		"A tool call the current mode does not allow returns an error saying so.",
		"This version has no shell or file-modifying tools.",
	}, "\n")
}
