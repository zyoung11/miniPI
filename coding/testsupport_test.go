package coding

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"

	"pi/ai"
	icodingagent "pi/internal/codingagent"
)

// runtimeTestProvider is a scripted ai.Provider test double that records the
// transcript and options of every stream call.
type runtimeTestProvider struct {
	id       string
	stream   func(context.Context, ai.TranscriptContext, ai.StreamOptions) (*ai.AssistantMessageEventStream, error)
	calls    atomic.Int32
	mu       sync.Mutex
	messages []ai.Message
	options  ai.StreamOptions
}

// ID returns the provider id.
func (provider *runtimeTestProvider) ID() string { return provider.id }

// Close releases the provider.
func (provider *runtimeTestProvider) Close() error { return nil }

// Stream records the call and runs the scripted stream.
func (provider *runtimeTestProvider) Stream(ctx context.Context, transcript ai.TranscriptContext, options ai.StreamOptions) (*ai.AssistantMessageEventStream, error) {
	provider.calls.Add(1)
	provider.mu.Lock()
	provider.messages = transcript.Messages()
	provider.options = options
	provider.mu.Unlock()
	return provider.stream(ctx, transcript, options)
}

// runtimeTestMessage builds one assistant message fixture.
func runtimeTestMessage(provider, model, text string, reason ai.StopReason) *ai.AssistantMessage {
	content := []ai.AssistantContentBlock(nil)
	if text != "" {
		content = append(content, ai.TextContent{Text: text})
	}
	return &ai.AssistantMessage{Content: content, API: ai.APIOpenAICompletions, Provider: provider, Model: model, StopReason: reason}
}

// runtimeTestTextStream pushes one canned text stream and returns its final message.
func runtimeTestTextStream(provider, model, text string) (*ai.AssistantMessageEventStream, *ai.AssistantMessage) {
	start := runtimeTestMessage(provider, model, "", ai.StopReasonPending)
	partial := runtimeTestMessage(provider, model, text, ai.StopReasonPending)
	final := runtimeTestMessage(provider, model, text, ai.StopReasonStop)
	stream := ai.NewAssistantMessageEventStream()
	for _, event := range []ai.AssistantMessageEvent{
		ai.StartEvent{Partial: start},
		ai.TextStartEvent{ContentIndex: 0, Partial: partial},
		ai.TextDeltaEvent{ContentIndex: 0, Delta: text, Partial: partial},
		ai.TextEndEvent{ContentIndex: 0, Content: text, Partial: partial},
		ai.DoneEvent{Reason: ai.StopReasonStop, Message: final},
	} {
		if err := stream.Push(event); err != nil {
			panic(err)
		}
	}
	return stream, final
}

// newRuntimeTestServices builds an isolated Services instance for one test.
func newRuntimeTestServices(t *testing.T) *Services {
	t.Helper()
	services, err := NewServices(ServicesOptions{CWD: t.TempDir(), AgentDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(services.Close)
	return services
}

// collectRuntimeEvents drains one event stream into a slice.
func collectRuntimeEvents(ctx context.Context, stream *ai.AssistantMessageEventStream) []ai.AssistantMessageEvent {
	var events []ai.AssistantMessageEvent
	for event := range stream.Events(ctx) {
		events = append(events, event)
	}
	return events
}

// lateProviderText concatenates the text blocks of one assistant message.
func lateProviderText(message *ai.AssistantMessage) string {
	var text string
	for _, block := range message.Content {
		if part, ok := block.(ai.TextContent); ok {
			text += part.Text
		}
	}
	return text
}

// modelOperationBridgeProbe records the host actions WireModelOperations binds.
type modelOperationBridgeProbe struct {
	actions map[string]any
}

// SetHostAction records one host action.
func (probe *modelOperationBridgeProbe) SetHostAction(key string, action any) {
	probe.actions[key] = action
}

// PublishModelCatalog is inert for the probe.
func (*modelOperationBridgeProbe) PublishModelCatalog() {}

// typedWireBridge records host actions for the typed model wire tests.
type typedWireBridge struct{ actions map[string]any }

// SetHostAction records one host action.
func (probe *typedWireBridge) SetHostAction(key string, action any) { probe.actions[key] = action }

// PublishModelCatalog is inert for the probe.
func (*typedWireBridge) PublishModelCatalog() {}

// wireTypedModelOperations wires a Services model runtime to one probe bridge.
func wireTypedModelOperations(t *testing.T, services *Services) *typedWireBridge {
	t.Helper()
	probe := &typedWireBridge{actions: make(map[string]any)}
	runtime := services.ModelRuntime()
	detach := icodingagent.WireModelOperations(probe, icodingagent.ModelOperationBindings{
		ModelLookup: runtime.GetModel, ModelCatalog: runtime.GetModels, Registry: services.Registry().ModelRegistry, Classify: runtime.Classify, GenerateImages: runtime.GenerateImages,
	})
	t.Cleanup(detach)
	return probe
}

// typedWireState reads the registry state back through JSON as an extension does.
func typedWireState(t *testing.T, probe *typedWireBridge) map[string]any {
	t.Helper()
	state := probe.actions["getModelRegistryState"].(func() map[string]any)()
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

// typedWireModels reads one typed model list out of the registry state.
func typedWireModels(state map[string]any, key string) []map[string]any {
	var models []map[string]any
	list, _ := state[key].([]any)
	for _, entry := range list {
		models = append(models, entry.(map[string]any))
	}
	return models
}

// typedWireFind finds one model entry by provider and id.
func typedWireFind(models []map[string]any, provider, id string) map[string]any {
	for _, model := range models {
		if model["provider"] == provider && model["id"] == id {
			return model
		}
	}
	return nil
}

// context9789Capture records the provider request one prompt produces and
// returns a reader for it.
func context9789Capture(h *recoveryHarness, text string) func(*testing.T) []ai.Message {
	var request []ai.Message
	h.provider.responses = append(h.provider.responses, func(messages []ai.Message) *ai.AssistantMessage {
		request = messages
		return fauxReply(text, ai.StopReasonStop, 0)(messages)
	})
	return func(t *testing.T) []ai.Message {
		t.Helper()
		if request == nil {
			t.Fatal("expected provider request")
		}
		return request
	}
}

// context9789Prompt sends one prompt through the harness session.
func context9789Prompt(t *testing.T, h *recoveryHarness, text string) {
	t.Helper()
	if _, err := h.session.Prompt(t.Context(), text); err != nil {
		t.Fatal(err)
	}
}
