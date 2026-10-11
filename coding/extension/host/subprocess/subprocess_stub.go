// Package subprocess holds the type-level surface the core keeps for
// subprocess extension hosts. The minimal port loads no subprocess
// extensions, so every value here is inert: the types exist so session and
// interactive wiring compile against the same shapes Pi uses.
package subprocess

import (
	"context"
	"encoding/json"
	"net"
	"fmt"
	"sync"
	"time"

	"pi/agent"
	"pi/ai"
	"pi/coding/extension"
)

// UIPromptScope reports an in-flight UI prompt so a subprocess prompt can end it.
type UIPromptScope interface {
	BeginUIPrompt(kind extension.UIPromptKind, title string) (end func())
}

// PushProxy is one widget pushed from an extension process to the editor side.
type PushProxy struct {
	mu         sync.RWMutex
	lines      []string
	linesWidth int
	placement  string
	order      uint64
}

// Lines returns the widget's rendered lines and the width they were rendered at.
func (p *PushProxy) Lines() ([]string, int) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.lines, p.linesWidth
}

// Placement returns the editor side the widget is pinned to.
func (p *PushProxy) Placement() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.placement
}

// Order returns the widget's insertion order.
func (p *PushProxy) Order() uint64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.order
}

// CellStrategy is how a host cell runs its extensions.
type CellStrategy string

// ReloadCellReport is one host cell's outcome in a reload.
type ReloadCellReport struct {
	Key           string
	Strategy      CellStrategy
	Language      string
	Extensions    []string
	Hash          string
	BinaryPath    string
	Cached        bool
	BuildDuration time.Duration
	Replaced      bool
	Quarantined   bool
	Reason        string
}

// ToolInfo is one registered tool as the host reports it to a subprocess.
type ToolInfo struct {
	Name             string                     `json:"name"`
	Description      string                     `json:"description"`
	Parameters       json.RawMessage            `json:"parameters"`
	PromptGuidelines []string                   `json:"promptGuidelines,omitempty"`
	Exposure         extension.ToolExposure     `json:"exposure,omitempty"`
	Namespace        *extension.ToolNamespace   `json:"namespace,omitempty"`
	Annotations      *extension.ToolAnnotations `json:"annotations,omitempty"`
	SourceInfo       extension.SourceInfo       `json:"sourceInfo"`
	Source           string                     `json:"-"`
}

// CommandInfo is one registered command as the host reports it to a subprocess.
type CommandInfo struct {
	Name        string               `json:"name"`
	Description string               `json:"description,omitempty"`
	Source      string               `json:"source"`
	SourceInfo  extension.SourceInfo `json:"sourceInfo"`
}

// ReloadReport is the outcome of an extension host reload.
type ReloadReport struct {
	StartedAt time.Time
	Duration  time.Duration
	Cells     []ReloadCellReport
	Removed   []string
	Error     string
	// Issues lists, as "<path>: Failed to load extension: <error>", the
	// extensions that failed to resolve, build, start, or register.
	Issues []string
}

// DirectEntryAppend carries the id and timestamp an extension process already
// generated for a custom session entry.
type DirectEntryAppend struct {
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
}

// FormatCrashNotice renders the operator-facing text for an extension crash
// event so both crash surfaces share one wording.
func FormatCrashNotice(name string, delay time.Duration, disabled bool, reason string) string {
	switch {
	case disabled:
		return fmt.Sprintf("extension %q disabled: %s", name, reason)
	case reason != "":
		return fmt.Sprintf("extension %q crashed, restart in %s: %s", name, delay, reason)
	default:
		return fmt.Sprintf("extension %q crashed, restart in %s", name, delay)
	}
}

// terminalInputStateKey is the context key for the terminal input snapshot.
type terminalInputStateKey struct{}

// terminalInputState is the UI snapshot a remote input listener serializes.
type terminalInputState struct {
	editorText    string
	toolsExpanded bool
}

// WithTerminalInputState captures the UI values on the owner loop before a
// remote input listener runs.
func WithTerminalInputState(ctx context.Context, editorText string, toolsExpanded bool) context.Context {
	return context.WithValue(ctx, terminalInputStateKey{}, terminalInputState{editorText: editorText, toolsExpanded: toolsExpanded})
}

// TerminalInputState returns the snapshot WithTerminalInputState captured.
func TerminalInputState(ctx context.Context) (editorText string, toolsExpanded bool, ok bool) {
	state, ok := ctx.Value(terminalInputStateKey{}).(terminalInputState)
	if !ok {
		return "", false, false
	}
	return state.editorText, state.toolsExpanded, true
}

// Host is the subprocess extension host. The minimal port loads no
// subprocess extensions, so the host never starts and every method is inert.
type Host struct{}

// Invalidate tells loaded extensions their state is stale.
func (h *Host) Invalidate(message string) {}

// Shutdown stops the host and its extension processes.
func (h *Host) Shutdown(reason string) {}

// Reload rebuilds and restarts the loaded extensions.
func (h *Host) Reload(owner context.Context) ([]extension.Extension, error) { return nil, nil }

// UIBridge routes subprocess extension calls to the TUI. The minimal port has
// no subprocess extensions, so every method is inert.
type UIBridge struct {
	mu      sync.Mutex
	actions map[string]any

	// OnRunSignalChanged is called when a run begins or ends.
	OnRunSignalChanged func()
}

// SetUIContext binds the UI context subprocess UI calls reach.
func (b *UIBridge) SetUIContext(ctx extension.UIContext) {}

// SetInvalidate binds the render invalidation callback.
func (b *UIBridge) SetInvalidate(fn func()) {}

// SetNotifyFunc binds the notification callback.
func (b *UIBridge) SetNotifyFunc(fn func(message string, level string)) {}

// SetWidgetSyncFunc binds the widget sync callback.
func (b *UIBridge) SetWidgetSyncFunc(fn func(widgets map[string]*PushProxy)) {}

// SetReplacementHost binds the host a session replacement hands over to.
func (h *Host) SetReplacementHost(resolve func() *Host) {}

// EndInput tells the host no more user input is coming.
func (h *Host) EndInput() {}

// TerminateProcesses stops every extension process.
func (h *Host) TerminateProcesses() {}

// Runtime returns the registrations the extension host holds.
func (h *Host) Runtime() *extension.ExtensionRuntime { return nil }

// SetHostAction registers one host call handler. The minimal port loads no
// subprocess extensions, so no handler ever runs.
func (b *UIBridge) SetHostAction(name string, fn any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.actions == nil {
		b.actions = map[string]any{}
	}
	b.actions[name] = fn
}

// NewUIBridge creates a bridge with the invalidate callback it forwards.
func NewUIBridge(invalidate func()) *UIBridge { return &UIBridge{} }

// ErrorInfo is one extension call error.
type ErrorInfo struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
}

// CallResultPayload is one extension call's result.
type CallResultPayload struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  *ErrorInfo      `json:"error,omitempty"`
}

// CallPayload is one extension call's request.
type CallPayload struct {
	Method string          `json:"method"`
	Args   json.RawMessage `json:"args,omitempty"`
}

// CallUIRegisterSprite is the host call that registers a login sprite.
const CallUIRegisterSprite = "ui/registerSprite"

// HandleCall runs one extension host call. The minimal port loads no
// subprocess extensions, so every call resolves empty.
func (b *UIBridge) HandleCall(ext string, payload *CallPayload) (*CallResultPayload, error) {
	return &CallResultPayload{}, nil
}

// SendUserMessageOptions carries the delivery behavior of an extension user
// message.
type SendUserMessageOptions struct {
	DeliverAs string `json:"deliverAs,omitempty"`
}

// MsgNotify is the envelope type carrying a notify call.
const MsgNotify = "notify"

// NotifyPayload is the payload of a notify call.
type NotifyPayload struct {
	Method string `json:"method"`
}

// Envelope is one framed message on an extension connection.
type Envelope struct {
	Type   string         `json:"type"`
	Notify *NotifyPayload `json:"notify,omitempty"`
}

// Conn is one extension process connection.
type Conn struct {
	name string
}

// NewConn wraps a network stream as an extension connection.
func NewConn(name string, c net.Conn) *Conn { return &Conn{name: name} }

// Start begins reading envelopes from the connection.
func (c *Conn) Start(ctx context.Context) {}

// Send writes one envelope to the connection.
func (c *Conn) Send(e *Envelope) error { return nil }

// Close stops the connection.
func (c *Conn) Close(reason string) error { return nil }

// RegisterExtConn registers one extension connection on the bridge.
func (b *UIBridge) RegisterExtConn(name string, conn *Conn) {}

// ModelCatalog returns the projected model catalog. The minimal port loads no
// extensions, so the catalog stays empty.
func (b *UIBridge) ModelCatalog() []map[string]any { return nil }

// SendMessageOptions carries the delivery behavior of an extension message.
type SendMessageOptions struct {
	TriggerTurn *bool  `json:"triggerTurn,omitempty"`
	DeliverAs   string `json:"deliverAs,omitempty"`
}

// UnavailableNestedCall is the outcome of a nested tool call in a context that
// does not support one.
func UnavailableNestedCall(callerID, name string) extension.AgentToolCallOutcome {
	return extension.AgentToolCallOutcome{
		ToolCall: ai.ToolCall{ID: callerID + "/0", Name: name, Arguments: ai.JsonObject{}},
		Result: agent.AgentToolResult{
			Content: []ai.ToolResultMessageContent{ai.TextContent{Text: "Nested tool calls are not available in this context"}},
			Details: map[string]any{},
		},
	}
}

// BindCommandActions binds the command actions extensions call. The minimal
// port loads no extensions.
func (b *UIBridge) BindCommandActions(actions extension.CommandActions) {}

// RunSignal returns the active run's cancellation. The minimal port binds no
// run signal.
func (b *UIBridge) RunSignal() context.Context { return nil }

// SessionStatePayload is the session part of a UI state snapshot.
type SessionStatePayload struct {
	SessionID   string `json:"sessionId,omitempty"`
	SessionName string `json:"sessionName"`
	SessionFile string `json:"sessionFile"`
	LeafID      string `json:"leafId"`
}

// StatePayload is one UI state snapshot for an extension process.
type StatePayload struct {
	Session *SessionStatePayload `json:"session,omitempty"`
}

// Snapshot returns the current UI state. The minimal port loads no extensions.
func (b *UIBridge) Snapshot(flagNames []string, cursor int, wantSessionLog bool) *StatePayload {
	return &StatePayload{}
}
