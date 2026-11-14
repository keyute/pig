package pigruntime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/MichaelKinsy/PiG/agent"
	"github.com/MichaelKinsy/PiG/ai"

	"github.com/keyute/kopi/internal/event"
)

func translate(ev agent.AgentEvent) (event.Event, bool) {
	switch ev := ev.(type) {
	case agent.MessageUpdateEvent:
		switch d := ev.AssistantMessageEvent.(type) {
		case ai.TextDeltaEvent:
			return event.TextDelta{Text: d.Delta}, true
		case ai.ThinkingDeltaEvent:
			return event.ThinkingDelta{Text: d.Delta}, true
		}
	case agent.ToolExecutionStartEvent:
		return event.ToolStart{CallID: ev.ToolCallID, Name: ev.ToolName, Args: compactJSON(ev.Args)}, true
	case agent.ToolExecutionEndEvent:
		return event.ToolEnd{CallID: ev.ToolCallID, Name: ev.ToolName, Output: toolResultContentText(ev.Result.Content), IsError: ev.IsError}, true
	case agent.AutoRetryStartEvent:
		delay := time.Duration(ev.DelayMs) * time.Millisecond
		return event.Notice{Text: fmt.Sprintf("retrying (attempt %d/%d) in %s: %s", ev.Attempt, ev.MaxAttempts, delay, ev.ErrorMessage)}, true
	case agent.CompactionStartEvent:
		return event.Notice{Text: fmt.Sprintf("compacting context (%s)", ev.Reason)}, true
	case agent.CompactionEndEvent:
		switch {
		case ev.Aborted:
			return event.Notice{Text: "compaction aborted"}, true
		case ev.ErrorMessage != "":
			return event.Notice{Text: "compaction failed: " + ev.ErrorMessage}, true
		}
		return event.Notice{Text: "compaction done"}, true
	}
	return nil, false
}

func compactJSON(raw json.RawMessage) string {
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return string(raw)
	}
	return b.String()
}

func toolResultContentText(content []ai.ToolResultMessageContent) string {
	var parts []string
	for _, c := range content {
		if text, ok := c.(ai.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}
