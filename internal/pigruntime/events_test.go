package pigruntime

import (
	"encoding/json"
	"testing"

	"github.com/MichaelKinsy/PiG/agent"
	"github.com/MichaelKinsy/PiG/ai"
	"github.com/stretchr/testify/assert"

	"github.com/keyute/kopi/internal/event"
)

func TestTranslate(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   agent.AgentEvent
		want event.Event
	}{
		{
			name: "text delta",
			in:   agent.MessageUpdateEvent{AssistantMessageEvent: ai.TextDeltaEvent{Delta: "hi"}},
			want: event.TextDelta{Text: "hi"},
		},
		{
			name: "thinking delta",
			in:   agent.MessageUpdateEvent{AssistantMessageEvent: ai.ThinkingDeltaEvent{Delta: "hmm"}},
			want: event.ThinkingDelta{Text: "hmm"},
		},
		{
			name: "tool start compacts args",
			in:   agent.ToolExecutionStartEvent{ToolCallID: "c1", ToolName: "Read", Args: json.RawMessage("{ \"file_path\": \"a.go\" }")},
			want: event.ToolStart{CallID: "c1", Name: "Read", Args: `{"file_path":"a.go"}`},
		},
		{
			name: "tool end flattens text",
			in: agent.ToolExecutionEndEvent{
				ToolCallID: "c1", ToolName: "Read", IsError: true,
				Result: agent.AgentToolResult{Content: []ai.ToolResultMessageContent{ai.TextContent{Text: "a"}, ai.ImageContent{}, ai.TextContent{Text: "b"}}},
			},
			want: event.ToolEnd{CallID: "c1", Name: "Read", Output: "a\nb", IsError: true},
		},
		{
			name: "auto retry",
			in:   agent.AutoRetryStartEvent{Attempt: 2, MaxAttempts: 3, DelayMs: 1500, ErrorMessage: "overloaded"},
			want: event.Notice{Text: "retrying (attempt 2/3) in 1.5s: overloaded"},
		},
		{
			name: "compaction start",
			in:   agent.CompactionStartEvent{Reason: "threshold"},
			want: event.Notice{Text: "compacting context (threshold)"},
		},
		{name: "compaction done", in: agent.CompactionEndEvent{}, want: event.Notice{Text: "compaction done"}},
		{name: "compaction aborted", in: agent.CompactionEndEvent{Aborted: true}, want: event.Notice{Text: "compaction aborted"}},
		{
			name: "compaction failed",
			in:   agent.CompactionEndEvent{ErrorMessage: "too big"},
			want: event.Notice{Text: "compaction failed: too big"},
		},
		{name: "message update without delta dropped", in: agent.MessageUpdateEvent{AssistantMessageEvent: ai.TextStartEvent{}}},
		{name: "agent end dropped", in: agent.AgentEndEvent{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := translate(tc.in)
			assert.Equal(t, tc.want != nil, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}
