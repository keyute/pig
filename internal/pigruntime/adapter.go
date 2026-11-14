package pigruntime

import (
	"context"
	"encoding/json"

	"github.com/MichaelKinsy/PiG/agent"
	"github.com/MichaelKinsy/PiG/ai"

	"github.com/keyute/kopi/internal/policy"
	"github.com/keyute/kopi/internal/tool"
)

type agentTool struct {
	tool tool.Tool
	gate *policy.Gate
}

func (a agentTool) Name() string  { return a.tool.Name }
func (a agentTool) Label() string { return a.tool.Label }

func (a agentTool) Schema() ai.ToolSchema {
	return ai.ToolSchema{Name: a.tool.Name, Description: a.tool.Description, Parameters: a.tool.Parameters}
}

// Execute admits the call against the grant recorded by the before-tool hook:
// PiG runs every hook of a parallel batch before executing any call, so the
// mode may have changed in between.
func (a agentTool) Execute(ctx context.Context, toolCallID string, params json.RawMessage, _ agent.ToolUpdateCallback) (agent.AgentToolResult, error) {
	if err := a.gate.Admit(toolCallID, a.tool.Name); err != nil {
		return errorResult(err), nil
	}
	out, err := a.tool.Run(ctx, params)
	if err != nil {
		return errorResult(err), nil
	}
	return agent.AgentToolResult{Content: []ai.ToolResultMessageContent{ai.TextContent{Text: out}}}, nil
}

func (a agentTool) ExecutionMode() agent.ToolExecutionMode {
	if a.tool.Capability == policy.Read {
		return agent.ToolModeParallel
	}
	return agent.ToolModeSequential
}

func beforeToolCall(gate *policy.Gate) agent.BeforeToolCallHook {
	return func(_ context.Context, toolCallID, toolName string, _ json.RawMessage) agent.ToolCallHookResult {
		if err := gate.Before(toolCallID, toolName); err != nil {
			return agent.ToolCallHookResult{Block: true, Reason: err.Error()}
		}
		return agent.ToolCallHookResult{}
	}
}

func errorResult(err error) agent.AgentToolResult {
	return agent.AgentToolResult{Content: []ai.ToolResultMessageContent{ai.TextContent{Text: err.Error()}}, IsError: true}
}
