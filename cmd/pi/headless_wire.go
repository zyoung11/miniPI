package main

import (
	"encoding/json"
	"io"

	"pi/agent"
	"pi/coding"
	"pi/coding/extension/host/subprocess"
)

// writeJSONLine writes v as one JSON line.
func writeJSONLine(w io.Writer, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	data = append(data, '\n')
	_, _ = w.Write(data)
}

// rpcAgentEvent converts one agent event to the JSON/RPC wire shapes in
// source insertion order, upstream's toJsonEvent.
func rpcAgentEvent(event agent.AgentEvent) ([]any, error) {
	switch event := event.(type) {
	case agent.AgentStartEvent:
		return []any{map[string]any{"type": "agent_start"}}, nil
	case agent.AgentEndEvent:
		return []any{map[string]any{"type": "agent_end", "willRetry": event.WillRetry}}, nil
	case agent.AgentSettledEvent:
		return []any{map[string]any{"type": "agent_settled"}}, nil
	case agent.BashExecutionUpdateEvent:
		return []any{map[string]any{"type": "bash_execution_update", "delta": event.Delta}}, nil
	case agent.QueueUpdateEvent:
		return []any{map[string]any{"type": "queue_update", "steering": event.Steering, "followUp": event.FollowUp}}, nil
	case agent.SessionInfoChangedEvent:
		return []any{map[string]any{"type": "session_info_changed", "name": event.Name}}, nil
	case agent.ThinkingLevelChangedEvent:
		return []any{map[string]any{"type": "thinking_level_changed", "level": event.Level}}, nil
	case agent.CompactionStartEvent:
		return []any{map[string]any{"type": "compaction_start", "reason": event.Reason}}, nil
	case agent.CompactionEndEvent:
		return []any{map[string]any{"type": "compaction_end", "reason": event.Reason}}, nil
	default:
		return nil, nil
	}
}

// subscribeStdoutBackpressure awaits the pending stdout writes before the next
// agent event. The minimal port queues nothing itself, so nothing to await.
func subscribeStdoutBackpressure(a *agent.Agent, wait func()) func() {
	return func() {}
}

// wireSubprocessModelRegistry forwards extension model registrations. The
// minimal port loads no extensions.
func wireSubprocessModelRegistry(bridge *subprocess.UIBridge, session *coding.Session, services *coding.Services) func() {
	return func() {}
}

// watchSessionRunSignal forwards the session run signal to extensions. The
// minimal port loads no extensions.
func watchSessionRunSignal(bridge *subprocess.UIBridge, session *coding.Session) func() {
	return func() {}
}

// stopAutomaticExtensionCacheGC stops the extension cache janitor. The minimal
// port runs no extension cache. A variable so tests can observe exits.
var stopAutomaticExtensionCacheGC = func() {}

// stopStartupExtensions releases the startup extension host. The minimal port
// starts no extension host. A variable so tests can observe exits.
var stopStartupExtensions = func() {}
