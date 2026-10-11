package codingagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	"pi/agent"
	"pi/ai"
	"pi/coding/extension"
	"pi/internal/codingagent/tools"
)

func (m *InteractiveMode) deliverUserMessage(content any, deliverAs extension.DeliverAs) error {
	parsed, err := subprocessUserContent(content)
	if err != nil {
		return fmt.Errorf("sendUserMessage: %w", err)
	}
	var text string
	var images []ai.ImageContent
	switch value := parsed.(type) {
	case ai.UserText:
		text = string(value)
	case ai.UserContentBlocks:
		var parts []string
		for _, block := range value {
			switch block := block.(type) {
			case ai.TextContent:
				parts = append(parts, block.Text)
			case ai.ImageContent:
				images = append(images, block)
			}
		}
		text = strings.Join(parts, "\n")
	}
	if m.agent == nil {
		return fmt.Errorf("agent not available")
	}
	switch deliverAs {
	case "", extension.DeliverAsFollowUp, extension.DeliverAsSteer:
		// validated; dispatched on the main loop below
	default:
		return fmt.Errorf("unsupported deliverAs %q", deliverAs)
	}

	// Host callbacks may run off the main loop. Marshal onto the UI task queue,
	// where TUI/session mutation is safe.
	//
	// turnActive, not isIdle, decides whether a turn exists to receive the
	// message. The run goroutine clears turnActive before it queues the cleanup
	// that restores isIdle, so a task landing in that window reads isIdle false
	// with no goroutine left to drain a steering queue, and the message is lost.
	// Upstream prompt() throws when a run is active and no streaming
	// behavior was given.
	if deliverAs == "" && m.runStreaming() {
		return errors.New("Agent is already processing. Specify streamingBehavior ('steer' or 'followUp') to queue the message.")
	}
	return m.postToMain(m.runCtx, func() {
		if m.agent == nil {
			return
		}
		// Upstream sendUserMessage is prompt() with source "extension" and
		// no command dispatch or template expansion. A run that started
		// after the check above receives an unspecified delivery as a
		// follow-up.
		m.promptUserInput(m.runCtx, text, images, deliverAs != extension.DeliverAsSteer, extension.InputSourceExtension, false)
	})
}

// activeBuiltinToolNames returns the names of built-in tools that are
// currently active (i.e., that refreshAgentTools will actually create and
// add to the agent). If ActiveBuiltinTools is nil, all built-in tools are
// active. Otherwise, only those in the set are active.
//
// This is used by GetAllTools/GetActiveTools host callbacks so that a host
// scoping sees built-in tools alongside extension tools. Without this, the
// package extension's applyScoping would build an AllowedTools set from only
// extension tools, causing refreshAgentTools to filter out all built-in tools
// (bash, read, write, edit, grep, find, ls) after a /reload or rescope.
func (m *InteractiveMode) activeBuiltinToolNames(extTools ...extension.RegisteredTool) []string {
	all := tools.BuiltinToolNames()
	overridden := make(map[string]bool, len(extTools))
	for _, tool := range extTools {
		overridden[tool.Definition.Name] = true
	}
	active := make([]string, 0, len(all))
	for _, name := range all {
		if !overridden[name] && tools.BuiltinToolActive(name, m.opts.ActiveBuiltinTools, m.opts.AllowedTools) {
			active = append(active, name)
		}
	}
	return active
}

// activeToolNames returns the tools the agent sends with its next request, in
// that order. Mirrors upstream AgentSession.getActiveToolNames
// (agent.state.tools): a tool an extension deactivated with setActiveTools is
// not reported, so a getActiveTools/setActiveTools round trip keeps it off.
func (m *InteractiveMode) activeToolNames() []string {
	if m.agent == nil {
		return m.activeBuiltinToolNames()
	}
	active := m.agent.Tools()
	names := make([]string, len(active))
	for i, tool := range active {
		names[i] = tool.Name()
	}
	return names
}

// sessionModel is the Session's model, read live as Pi's extension context reads agent.state.model (runner.ts createContext `get model()`, agent-session.ts _bindExtensionCore). The Session applies a change before it emits model_select and thinking_level_select; the mode's own copy follows only after the Session returns.
func (m *InteractiveMode) sessionModel() *ai.Model {
	if m.opts.SessionHandle != nil {
		if session := m.opts.SessionHandle.Agent(); session != nil {
			if model := session.Model(); model != nil {
				return model
			}
		}
	}
	if m.agent != nil {
		if model := m.agent.Model(); model != nil {
			return model
		}
	}
	return m.opts.Model
}

// extensionContextUsage reads the same Session projection as the stock footer. A zero window means no usage; a nil estimate with a usable window means unknown usage after compaction.
func (m *InteractiveMode) extensionContextUsage() *extension.ContextUsage {
	if m.opts.ContextUsage == nil {
		return nil
	}
	tokens, window := m.opts.ContextUsage()
	if window <= 0 {
		return nil
	}
	usage := &extension.ContextUsage{Tokens: tokens, ContextWindow: window}
	if tokens != nil {
		percent := float64(*tokens) / float64(window) * 100
		usage.Percent = &percent
	}
	return usage
}

// wireInprocContextActions binds the same session state as wireSubprocessHostCallbacks to direct extension context calls.
func (m *InteractiveMode) wireInprocContextActions() {
	m.bindEditorSnapshot()
	if m.newRunner == nil {
		return
	}
	m.newRunner.AddErrorListener(m.queueExtensionError)
	var ui extension.UIContext
	if m.tuiInst != nil && m.layout != nil {
		ui = &ExtUIContext{m: m}
	}
	m.newRunner.SetUIContext(ui, extension.ModeTUI)
	// Subprocess dialogs reach the TUI through the bridge, so the bridge
	// reports their ui_prompt_start/ui_prompt_end through the current runner.
	if m.opts.SubprocessUIBridge != nil {
		m.opts.SubprocessUIBridge.SetUIPromptScope(m.newRunner)
	}

	actions := extension.ContextActions{
		GetScopedModels: m.extensionScopedModels,
		GetAllTools: func() []extension.ToolInfo {
			// upstream: agent-session.ts:3353 (getAllTools): the Session's registry, with each tool's source, exposure and namespace.
			if session, ok := m.opts.SessionHandle.(interface{ GetAllTools() []extension.ToolInfo }); ok {
				return session.GetAllTools()
			}
			extTools := m.newRunner.Tools()
			builtinNames := m.activeBuiltinToolNames(extTools...)
			result := make([]extension.ToolInfo, 0, len(extTools)+len(builtinNames))
			for _, t := range extTools {
				source := "builtin"
				if s, ok := t.SourceInfo.(string); ok && s != "" {
					source = s
				}
				result = append(result, extension.ToolInfo{
					Name:        t.Definition.Name,
					Description: t.Definition.Description,
					SourceInfo:  source,
				})
			}
			// Append built-in tools so package scoping sees them. Built-in
			// tools live in the agent (not the runner), so without this they
			// would be invisible to GetAllTools → ScopeTools → SetActiveTools,
			// causing them to be filtered out after a rescope.
			for _, name := range builtinNames {
				result = append(result, extension.ToolInfo{
					Name:        name,
					Description: tools.BuiltinToolDescription(name),
					SourceInfo:  "builtin",
				})
			}
			return result
		},
		GetActiveTools: m.activeToolNames,
		SetActiveTools: m.setActiveToolsByName,
		RefreshTools:   func() error { return m.refreshSessionTools(false) },
		GetFlagValue: func(name string) any {
			if m.opts.UnknownFlags != nil {
				if value, ok := m.opts.UnknownFlags[name]; ok {
					return value
				}
			}
			return nil
		},
		GetModel: func() extension.Model {
			model := m.sessionModel()
			if model == nil {
				return nil
			}
			return modelToExtModel(model)
		},
		IsIdle:           m.extensionIsIdle,
		GetSignal:        m.extensionSignal,
		IsProjectTrusted: func() bool { return m.projectTrusted() },
		HasPendingMessages: func() bool {
			if m.agent == nil {
				return false
			}
			steering, followUps := m.agent.PendingMessages()
			return len(steering)+len(followUps) > 0
		},
		Abort: func() {
			if m.agent != nil {
				m.abortFn()
			}
		},
		Shutdown:        m.requestExtensionShutdown,
		GetContextUsage: m.extensionContextUsage,
		GetSystemPrompt: func() string {
			return m.currentSystemPrompt()
		},
		GetSystemPromptOptions: m.currentSystemPromptOptions,
	}

	m.newRunner.BindCore(
		extension.ExtensionActions{
			SendUserMessage: func(content any, opts *extension.SendUserMessageOptions) error {
				var deliverAs extension.DeliverAs
				if opts != nil {
					deliverAs = opts.DeliverAs
				}
				return m.deliverUserMessage(content, deliverAs)
			},
		},
		actions,
		nil,
	)
	m.bindExtensionCommandActions()
}

// setActiveToolsByName activates the named registered tools in the requested
// order and ignores unknown names. As upstream selects from the whole tool
// registry, a built-in outside the startup active set can be activated; the
// --tools allowlist and --exclude-tools still bound the registry.
// Mirrors upstream AgentSession.setActiveToolsByName.
func (m *InteractiveMode) setActiveToolsByName(names []string) {
	m.selectActiveToolsByName(names)
	m.rebuildToolSystemPrompt()
	m.refreshForcedPrompt()
}

func (m *InteractiveMode) selectActiveToolsByName(names []string) {
	// The Session owns the tool registry and the loadout (exposure, namespaces, declarations): it admits the names, and
	// this mode keeps the allow-list its own rebuilds apply.
	if session, ok := m.opts.SessionHandle.(interface {
		SetActiveToolsByName([]string)
		ActiveToolNames() []string
	}); ok {
		session.SetActiveToolsByName(names)
		active := session.ActiveToolNames()
		m.opts.AllowedTools = make(map[string]struct{}, len(active))
		for _, name := range active {
			m.opts.AllowedTools[name] = struct{}{}
		}
		return
	}
	m.opts.AllowedTools = make(map[string]struct{}, len(names))
	for _, name := range names {
		if m.opts.ToolRegistryAllowed != nil {
			if _, allowed := m.opts.ToolRegistryAllowed[name]; !allowed {
				continue
			}
		}
		m.opts.AllowedTools[name] = struct{}{}
	}
	tools, errs := m.buildAgentTools()
	for _, err := range errs {
		fmt.Fprintf(os.Stderr, "tool refresh: %v\n", err)
	}
	if m.agent == nil {
		return
	}
	available := make(map[string]agent.AgentTool)
	for _, tool := range tools {
		available[tool.Name()] = tool
	}
	var selected []agent.AgentTool
	for _, name := range names {
		if tool := available[name]; tool != nil {
			selected = append(selected, tool)
		}
	}
	m.agent.SetTools(selected)
}

// bindExtensionCommandActions binds the interactive command-context actions
// over the Session's own, for in-process and subprocess extensions.
func (m *InteractiveMode) bindExtensionCommandActions() {
	actions := m.extensionReplacementActions()
	actions.NavigateTree = m.extensionNavigateTree
	actions.NavigateTreeContext = m.extensionNavigateTreeContext
	if m.newRunner != nil {
		m.newRunner.BindCommandActions(actions)
	}
	if m.opts.SubprocessUIBridge != nil {
		bridge := m.opts.SubprocessUIBridge
		bridge.SetHostAction("navigateTree", actions.NavigateTreeContext)
		if actions.NewSessionContext != nil {
			bridge.SetHostAction("newSession", actions.NewSessionContext)
			bridge.SetHostAction("fork", actions.ForkContext)
			bridge.SetHostAction("switchSession", actions.SwitchSessionContext)
		}
	}
}

// extensionNavigateTree is ctx.navigateTree in interactive mode: navigate the
// Session, then redraw the chat, fill an empty editor with the editor text
// and report it (interactive-mode.ts commandContextActions.navigateTree).
func (m *InteractiveMode) extensionNavigateTree(targetID string, opts *extension.NavigateTreeOptions) (extension.CancelledResult, error) {
	return m.extensionNavigateTreeContext(context.Background(), targetID, opts)
}

func (m *InteractiveMode) extensionNavigateTreeContext(ctx context.Context, targetID string, opts *extension.NavigateTreeOptions) (extension.CancelledResult, error) {
	if m.opts.SessionHandle == nil {
		return extension.CancelledResult{Cancelled: true}, nil
	}
	var summarize bool
	var customInstructions string
	if opts != nil {
		summarize, customInstructions = opts.Summarize, opts.CustomInstructions
	}
	result, err := m.opts.SessionHandle.NavigateTreeHandle(ctx, targetID, summarize, customInstructions)
	if err != nil {
		return extension.CancelledResult{}, err
	}
	if result.Cancelled {
		return extension.CancelledResult{Cancelled: true}, nil
	}
	m.runOnMain(m.runCtx, func() {
		m.rebuildChatFromSession()
		if result.EditorText != "" && strings.TrimSpace(m.editor.Text()) == "" {
			m.editor.SetText(result.EditorText)
		}
		m.showStatus("Navigated to selected point")
		m.tuiInst.Render()
	})
	return extension.CancelledResult{}, nil
}

// publishSlashCommandCatalog republishes the prompt templates, skills and
// resource provenance pi.getCommands() lists. Call it on the owner loop after
// changing any of them: extension host calls read the published copy, never
// the live fields.
func (m *InteractiveMode) publishSlashCommandCatalog() {
	catalog := &SlashCommandCatalog{
		PromptTemplates: append([]PromptTemplate(nil), m.promptTemplates...),
		Skills:          append([]*SkillDef(nil), m.opts.Skills...),
		CWD:             m.opts.CWD,
		AgentDir:        m.opts.AgentDir,
		SourceInfo:      cloneResourceSourceInfoMap(m.resourceSourceInfo),
	}
	m.slashCatalog.Store(catalog)
}

// sessionToolActions is the session's nested-call actions. Only a session that runs tools has them; the handle is read at call time because a replacement swaps it.
func (m *InteractiveMode) sessionToolActions() (extension.ToolActions, bool) {
	session, ok := m.opts.SessionHandle.(interface{ ToolActions() extension.ToolActions })
	if !ok {
		return extension.ToolActions{}, false
	}
	return session.ToolActions(), true
}

func latestCompactionIndex(entries []SessionEntry) int {
	for i, entrie := range slices.Backward(entries) {
		if entrie.Base.Type == "compaction" {
			return i
		}
	}
	return -1
}

func hasAssistantUsageAfter(entries []SessionEntry, index int) bool {
	for i := len(entries) - 1; i > index; i-- {
		msg, ok := entries[i].AsMessage()
		if !ok || msg.Message.Assistant == nil {
			continue
		}
		assistant := msg.Message.Assistant
		if assistant.StopReason == "aborted" || assistant.StopReason == "error" {
			return false
		}
		usage := assistant.Usage
		if usage == nil {
			return false
		}
		return usage.TotalTokens > 0 || usage.Input+usage.Output+usage.CacheRead+usage.CacheWrite > 0
	}
	return false
}

func sessionEntriesRawPage(entries []SessionEntry, cursor, maxBytes int) ([]json.RawMessage, int) {
	if cursor < 0 || cursor > len(entries) {
		cursor = 0
	}
	if cursor == len(entries) {
		return nil, cursor
	}
	out := make([]json.RawMessage, 0)
	bytes := 0
	next := cursor
	for next < len(entries) {
		raw := entries[next].Raw()
		entryBytes := len(raw) + 1
		if len(out) > 0 && bytes+entryBytes > maxBytes {
			break
		}
		if len(raw) > 0 {
			out = append(out, json.RawMessage(raw))
			bytes += entryBytes
		}
		next++
	}
	return out, next
}

func subprocessModelSpec(model map[string]any) string {
	providerID := ""
	switch p := model["provider"].(type) {
	case string:
		providerID = p
	case map[string]any:
		providerID, _ = p["id"].(string)
	}
	modelID, _ := model["modelId"].(string)
	if modelID == "" {
		if id, _ := model["id"].(string); id != "" {
			if before, after, ok := strings.Cut(id, "/"); ok {
				if providerID == "" {
					providerID = before
				}
				modelID = after
			} else {
				modelID = id
			}
		}
	}
	if providerID == "" {
		return modelID
	}
	return providerID + "/" + modelID
}

func subprocessRequiredString(object map[string]any, key, path string) (string, error) {
	value, exists := object[key]
	if !exists {
		return "", fmt.Errorf("%s.%s is required", path, key)
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s.%s must be a string", path, key)
	}
	return text, nil
}

func subprocessOptionalString(object map[string]any, key, path string) (string, error) {
	value, exists := object[key]
	if !exists {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s.%s must be a string", path, key)
	}
	return text, nil
}

func subprocessOptionalInt64(object map[string]any, key, path string) (int64, error) {
	value, exists := object[key]
	if !exists {
		return 0, nil
	}
	switch number := value.(type) {
	case float64:
		if number != float64(int64(number)) {
			return 0, fmt.Errorf("%s.%s must be an integer", path, key)
		}
		return int64(number), nil
	case int:
		return int64(number), nil
	case int64:
		return number, nil
	default:
		return 0, fmt.Errorf("%s.%s must be an integer", path, key)
	}
}

func subprocessDecodeJSON(value any, target any, path string) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if err := json.Unmarshal(encoded, target); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func subprocessRequestMessages(raw any) ([]ai.Message, error) {
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("model request messages must be an array")
	}
	messages := make([]ai.Message, 0, len(items))
	for index, item := range items {
		message, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("model request messages[%d] must be an object", index)
		}
		role, err := subprocessRequiredString(message, "role", fmt.Sprintf("model request messages[%d]", index))
		if err != nil {
			return nil, err
		}
		path := fmt.Sprintf("model request messages[%d]", index)
		switch role {
		case "system":
			var content ai.SystemContent
			switch rawContent := message["content"].(type) {
			case string:
				content = ai.SystemText(rawContent)
			case []any:
				blocks := make(ai.SystemTextBlocks, 0, len(rawContent))
				for blockIndex, rawBlock := range rawContent {
					block, ok := rawBlock.(map[string]any)
					if !ok || block["type"] != "text" {
						return nil, fmt.Errorf("%s.content[%d] must be a text block", path, blockIndex)
					}
					text, err := subprocessRequiredString(block, "text", fmt.Sprintf("%s.content[%d]", path, blockIndex))
					if err != nil {
						return nil, err
					}
					textSignature, err := subprocessOptionalString(block, "textSignature", fmt.Sprintf("%s.content[%d]", path, blockIndex))
					if err != nil {
						return nil, err
					}
					blocks = append(blocks, ai.TextContent{Text: text, TextSignature: textSignature})
				}
				content = blocks
			default:
				return nil, fmt.Errorf("%s.content must be a string or text-block array", path)
			}
			var sections ai.OrderedSections
			if rawSections, exists := message["sections"]; exists {
				switch rawSections := rawSections.(type) {
				case ai.OrderedSections:
					sections = rawSections
				default:
					if err := subprocessDecodeJSON(rawSections, &sections, path+".sections"); err != nil {
						return nil, err
					}
				}
			}
			var toolsAdded []ai.ToolSchema
			if rawTools, exists := message["toolsAdded"]; exists {
				if err := subprocessDecodeJSON(rawTools, &toolsAdded, path+".toolsAdded"); err != nil {
					return nil, err
				}
			}
			var toolsRemoved []ai.ToolReference
			if rawTools, exists := message["toolsRemoved"]; exists {
				if err := subprocessDecodeJSON(rawTools, &toolsRemoved, path+".toolsRemoved"); err != nil {
					return nil, err
				}
			}
			timestamp, err := subprocessOptionalInt64(message, "timestamp", path)
			if err != nil {
				return nil, err
			}
			messages = append(messages, ai.SystemMessage{Content: content, Sections: sections, ToolsAdded: toolsAdded, ToolsRemoved: toolsRemoved, Timestamp: timestamp})
		case "user":
			content, err := subprocessUserContent(message["content"])
			if err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			timestamp, err := subprocessOptionalInt64(message, "timestamp", path)
			if err != nil {
				return nil, err
			}
			messages = append(messages, ai.UserMessage{Content: content, Timestamp: timestamp})
		case "assistant":
			content, err := subprocessAssistantContent(message["content"])
			if err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			var fields struct {
				API                   ai.API                          `json:"api"`
				Provider              string                          `json:"provider"`
				Model                 string                          `json:"model"`
				ResponseModel         string                          `json:"responseModel"`
				ResponseID            string                          `json:"responseId"`
				ProviderThinkingLevel string                          `json:"providerThinkingLevel"`
				Diagnostics           []ai.AssistantMessageDiagnostic `json:"diagnostics"`
				Usage                 ai.Usage                        `json:"usage"`
				StopReason            ai.StopReason                   `json:"stopReason"`
				Deferred              *ai.DeferredHandle              `json:"deferred"`
				ErrorMessage          string                          `json:"errorMessage"`
				RawStopReason         string                          `json:"rawStopReason"`
				EndTurn               *bool                           `json:"endTurn"`
				Timestamp             int64                           `json:"timestamp"`
			}
			if err := subprocessDecodeJSON(message, &fields, path); err != nil {
				return nil, err
			}
			messages = append(messages, ai.AssistantMessage{Content: content, API: fields.API, Provider: fields.Provider, Model: fields.Model, ResponseModel: fields.ResponseModel, ResponseID: fields.ResponseID, ProviderThinkingLevel: fields.ProviderThinkingLevel, Diagnostics: fields.Diagnostics, Usage: fields.Usage, StopReason: fields.StopReason, Deferred: fields.Deferred, ErrorMessage: fields.ErrorMessage, RawStopReason: fields.RawStopReason, EndTurn: fields.EndTurn, Timestamp: fields.Timestamp})
		case "toolResult", "tool":
			content, err := subprocessToolResultContent(message["content"])
			if err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			var fields struct {
				ToolCallID string    `json:"toolCallId"`
				ToolName   string    `json:"toolName"`
				Details    any       `json:"details"`
				Usage      *ai.Usage `json:"usage"`
				IsError    bool      `json:"isError"`
				Timestamp  int64     `json:"timestamp"`
			}
			if err := subprocessDecodeJSON(message, &fields, path); err != nil {
				return nil, err
			}
			if fields.ToolCallID == "" {
				return nil, fmt.Errorf("%s.toolCallId is required", path)
			}
			_, hasDetails := message["details"]
			messages = append(messages, ai.ToolResultMessage{ToolCallID: fields.ToolCallID, ToolName: fields.ToolName, Content: content, Details: fields.Details, DetailsNull: hasDetails && fields.Details == nil, Usage: fields.Usage, IsError: fields.IsError, Timestamp: fields.Timestamp})
		default:
			return nil, fmt.Errorf("%s has unsupported role %q", path, role)
		}
	}
	return messages, nil
}

func subprocessUserContent(raw any) (ai.UserContent, error) {
	switch typed := raw.(type) {
	case ai.UserText:
		return typed, nil
	case ai.UserContentBlocks:
		return slices.Clone(typed), nil
	case []ai.UserContentBlock:
		return ai.UserContentBlocks(slices.Clone(typed)), nil
	}
	if text, ok := raw.(string); ok {
		return ai.UserText(text), nil
	}
	blocks, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("user content must be a string or array")
	}
	content := make(ai.UserContentBlocks, 0, len(blocks))
	for index, rawBlock := range blocks {
		block, ok := rawBlock.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("user content[%d] must be an object", index)
		}
		blockType, err := subprocessRequiredString(block, "type", fmt.Sprintf("user content[%d]", index))
		if err != nil {
			return nil, err
		}
		switch blockType {
		case "text":
			text, err := subprocessRequiredString(block, "text", fmt.Sprintf("user content[%d]", index))
			if err != nil {
				return nil, err
			}
			signature, err := subprocessOptionalString(block, "textSignature", fmt.Sprintf("user content[%d]", index))
			if err != nil {
				return nil, err
			}
			content = append(content, ai.TextContent{Text: text, TextSignature: signature})
		case "image":
			data, err := subprocessRequiredString(block, "data", fmt.Sprintf("user content[%d]", index))
			if err != nil {
				return nil, err
			}
			mimeType, err := subprocessOptionalString(block, "mimeType", fmt.Sprintf("user content[%d]", index))
			if err != nil {
				return nil, err
			}
			if mimeType == "" {
				mimeType, err = subprocessRequiredString(block, "media_type", fmt.Sprintf("user content[%d]", index))
				if err != nil {
					return nil, err
				}
			}
			content = append(content, ai.ImageContent{Data: data, MimeType: mimeType})
		default:
			return nil, fmt.Errorf("unsupported user content type %q", blockType)
		}
	}
	return content, nil
}

func subprocessAssistantContent(raw any) ([]ai.AssistantContentBlock, error) {
	blocks, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("assistant content must be an array")
	}
	content := make([]ai.AssistantContentBlock, 0, len(blocks))
	for index, rawBlock := range blocks {
		block, ok := rawBlock.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("assistant content[%d] must be an object", index)
		}
		path := fmt.Sprintf("assistant content[%d]", index)
		blockType, err := subprocessRequiredString(block, "type", path)
		if err != nil {
			return nil, err
		}
		switch blockType {
		case "text":
			text, err := subprocessRequiredString(block, "text", path)
			if err != nil {
				return nil, err
			}
			signature, err := subprocessOptionalString(block, "textSignature", path)
			if err != nil {
				return nil, err
			}
			content = append(content, ai.TextContent{Text: text, TextSignature: signature})
		case "thinking":
			thinking, err := subprocessRequiredString(block, "thinking", path)
			if err != nil {
				return nil, err
			}
			signature, err := subprocessOptionalString(block, "thinkingSignature", path)
			if err != nil {
				return nil, err
			}
			redacted := false
			if raw, exists := block["redacted"]; exists {
				var ok bool
				redacted, ok = raw.(bool)
				if !ok {
					return nil, fmt.Errorf("%s.redacted must be a boolean", path)
				}
			}
			content = append(content, ai.ThinkingContent{Thinking: thinking, ThinkingSignature: signature, Redacted: redacted})
		case "toolCall":
			id, err := subprocessRequiredString(block, "id", path)
			if err != nil || id == "" {
				if err != nil {
					return nil, err
				}
				return nil, fmt.Errorf("%s.id is required", path)
			}
			name, err := subprocessRequiredString(block, "name", path)
			if err != nil {
				return nil, err
			}
			arguments, ok := block["arguments"].(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%s.arguments must be an object", path)
			}
			thoughtSignature, err := subprocessOptionalString(block, "thoughtSignature", path)
			if err != nil {
				return nil, err
			}
			namespace, err := subprocessOptionalString(block, "namespace", path)
			if err != nil {
				return nil, err
			}
			content = append(content, ai.ToolCall{ID: id, Name: name, Arguments: ai.JsonObject(arguments), ThoughtSignature: thoughtSignature, Namespace: namespace})
		default:
			return nil, fmt.Errorf("unsupported assistant content type %q", blockType)
		}
	}
	return content, nil
}

func subprocessToolResultContent(raw any) ([]ai.ToolResultMessageContent, error) {
	if text, ok := raw.(string); ok {
		return []ai.ToolResultMessageContent{ai.TextContent{Text: text}}, nil
	}
	userContent, err := subprocessUserContent(raw)
	if err != nil {
		return nil, err
	}
	blocks := userContent.(ai.UserContentBlocks)
	content := make([]ai.ToolResultMessageContent, len(blocks))
	for i, block := range blocks {
		content[i] = block.(ai.ToolResultMessageContent)
	}
	return content, nil
}

func subprocessOptionalFloat64(object map[string]any, key, path string) (float64, bool, error) {
	value, exists := object[key]
	if !exists {
		return 0, false, nil
	}
	switch number := value.(type) {
	case float64:
		return number, true, nil
	case int:
		return float64(number), true, nil
	default:
		return 0, true, fmt.Errorf("%s.%s must be a number", path, key)
	}
}

func subprocessStringMap(raw any, path string) (map[string]string, error) {
	object, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an object", path)
	}
	values := make(map[string]string, len(object))
	for key, rawValue := range object {
		value, ok := rawValue.(string)
		if !ok {
			return nil, fmt.Errorf("%s.%s must be a string", path, key)
		}
		values[key] = value
	}
	return values, nil
}

func subprocessProviderHeaders(raw any, path string) (ai.ProviderHeaders, error) {
	object, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an object", path)
	}
	values := make(ai.ProviderHeaders, len(object))
	for key, rawValue := range object {
		if rawValue == nil {
			values[key] = nil
			continue
		}
		value, ok := rawValue.(string)
		if !ok {
			return nil, fmt.Errorf("%s.%s must be a string or null", path, key)
		}
		values[key] = new(value)
	}
	return values, nil
}

func subprocessStreamOptions(request map[string]any, defaults ai.StreamOptions) (ai.StreamOptions, error) {
	options := defaults
	for _, field := range []struct {
		name   string
		target **int
	}{
		{"timeoutMs", &options.TimeoutMs}, {"websocketConnectTimeoutMs", &options.WebSocketConnectTimeoutMs}, {"maxRetries", &options.MaxRetries}, {"maxRetryDelayMs", &options.MaxRetryDelayMs},
	} {
		if _, exists := request[field.name]; !exists {
			continue
		}
		value, err := subprocessOptionalInt64(request, field.name, "model request")
		if err != nil {
			return ai.StreamOptions{}, err
		}
		if value < 0 {
			return ai.StreamOptions{}, fmt.Errorf("model request.%s must be non-negative", field.name)
		}
		*field.target = new(int(value))
	}
	if value, exists := request["maxTokens"]; exists {
		number, err := subprocessOptionalInt64(request, "maxTokens", "model request")
		if err != nil {
			return ai.StreamOptions{}, err
		}
		if number < 0 {
			return ai.StreamOptions{}, fmt.Errorf("model request.maxTokens must be non-negative")
		}
		options.MaxTokens = int(number)
		_ = value
	}
	if value, exists, err := subprocessOptionalFloat64(request, "temperature", "model request"); err != nil {
		return ai.StreamOptions{}, err
	} else if exists {
		options.Temperature = value
		options.TemperatureSet = true
	}
	if raw, exists := request["samplingParams"]; exists {
		object, ok := raw.(map[string]any)
		if !ok {
			return ai.StreamOptions{}, fmt.Errorf("model request.samplingParams must be an object")
		}
		options.SamplingParams = object
	}
	if raw, exists := request["thinkingBudgets"]; exists {
		object, ok := raw.(map[string]any)
		if !ok {
			return ai.StreamOptions{}, fmt.Errorf("model request.thinkingBudgets must be an object")
		}
		budgets := &ai.ThinkingBudgets{}
		for key, target := range map[string]*int{"minimal": &budgets.Minimal, "low": &budgets.Low, "medium": &budgets.Medium, "high": &budgets.High} {
			if _, exists := object[key]; !exists {
				continue
			}
			value, err := subprocessOptionalInt64(object, key, "model request.thinkingBudgets")
			if err != nil {
				return ai.StreamOptions{}, err
			}
			*target = int(value)
		}
		options.ThinkingBudgets = budgets
	}
	if value, exists := request["thinking"]; exists {
		object, ok := value.(map[string]any)
		if !ok {
			return ai.StreamOptions{}, fmt.Errorf("model request.thinking must be an object")
		}
		data, err := json.Marshal(object)
		if err != nil {
			return ai.StreamOptions{}, err
		}
		var thinking ai.GoogleThinkingOptions
		if err := json.Unmarshal(data, &thinking); err != nil {
			return ai.StreamOptions{}, fmt.Errorf("model request.thinking: %w", err)
		}
		options.GoogleThinking = &thinking
	}
	for name, target := range map[string]**bool{"thinkingEnabled": &options.ThinkingEnabled, "interleavedThinking": &options.InterleavedThinking} {
		if value, exists := request[name]; exists {
			boolean, ok := value.(bool)
			if !ok {
				return ai.StreamOptions{}, fmt.Errorf("model request.%s must be a boolean", name)
			}
			*target = new(boolean)
		}
	}
	if _, exists := request["thinkingBudgetTokens"]; exists {
		value, err := subprocessOptionalInt64(request, "thinkingBudgetTokens", "model request")
		if err != nil {
			return ai.StreamOptions{}, err
		}
		options.ThinkingBudgetTokens = new(int(value))
	}
	if value, exists := request["effort"]; exists {
		text, ok := value.(string)
		if !ok {
			return ai.StreamOptions{}, fmt.Errorf("model request.effort must be a string")
		}
		options.Effort = text
	}
	if value, exists := request["requestMetadata"]; exists {
		metadata, err := subprocessStringMap(value, "model request.requestMetadata")
		if err != nil {
			return ai.StreamOptions{}, err
		}
		options.RequestMetadata = metadata
	}
	if value, exists := request["reasoningEffort"]; exists {
		text, ok := value.(string)
		if !ok {
			return ai.StreamOptions{}, fmt.Errorf("model request.reasoningEffort must be a string")
		}
		options.ReasoningEffort = text
	}
	if value, exists := request["reasoning"]; exists {
		// Upstream SimpleStreamOptions.reasoning: the thinking level of a
		// streamSimple/completeSimple request.
		text, ok := value.(string)
		if !ok {
			return ai.StreamOptions{}, fmt.Errorf("model request.reasoning must be a string")
		}
		options.Thinking = ai.ThinkingLevel(text)
	}
	if value, exists := request["apiKey"]; exists {
		// Upstream options.apiKey: the request's credential, ahead of the
		// configured one.
		text, ok := value.(string)
		if !ok {
			return ai.StreamOptions{}, fmt.Errorf("model request.apiKey must be a string")
		}
		options.APIKey = text
	}
	if value, exists := request["isReasoning"]; exists {
		boolean, ok := value.(bool)
		if !ok {
			return ai.StreamOptions{}, fmt.Errorf("model request.isReasoning must be a boolean")
		}
		options.IsReasoning = boolean
	}
	if raw, exists := request["env"]; exists {
		values, err := subprocessStringMap(raw, "model request.env")
		if err != nil {
			return ai.StreamOptions{}, err
		}
		options.Env = ai.ProviderEnv(values)
	}
	if raw, exists := request["headers"]; exists {
		values, err := subprocessProviderHeaders(raw, "model request.headers")
		if err != nil {
			return ai.StreamOptions{}, err
		}
		options.Headers = values
	}
	if value, exists := request["sessionId"]; exists {
		text, ok := value.(string)
		if !ok {
			return ai.StreamOptions{}, fmt.Errorf("model request.sessionId must be a string")
		}
		options.SessionID = text
	}
	if value, exists := request["transport"]; exists {
		text, ok := value.(string)
		if !ok {
			return ai.StreamOptions{}, fmt.Errorf("model request.transport must be a string")
		}
		options.Transport = ai.Transport(text)
	}
	if value, exists := request["cacheRetention"]; exists {
		text, ok := value.(string)
		if !ok {
			return ai.StreamOptions{}, fmt.Errorf("model request.cacheRetention must be a string")
		}
		options.CacheRetention = ai.CacheRetention(text)
	}
	options.ToolChoice = request["toolChoice"]
	if metadata, exists := request["metadata"]; exists {
		object, ok := metadata.(map[string]any)
		if !ok {
			return ai.StreamOptions{}, fmt.Errorf("model request.metadata must be an object")
		}
		options.Metadata = object
	}
	return options, nil
}

type ModelOperationBridge interface {
	SetHostAction(key string, fn any)
	PublishModelCatalog()
}

type modelCatalogEncodingBridge interface {
	SetModelCatalog(func() []map[string]any, func() (json.RawMessage, error))
}

type ModelOperationBindings struct {
	CurrentModel  func() *ai.Model
	ModelLookup   func(providerID, modelID string) *ai.Model
	ModelCatalog  func() []*ai.Model
	Registry      *ModelRegistry
	ModelBuilder  func(spec string) (*ai.Model, error)
	SessionHandle InteractiveSessionHandle
	// Classify is the Session runtime's classify; an extension's ctx.modelRegistry.classify reaches it. Nil answers an error result.
	Classify func(context.Context, *ai.ClassifierModel, ai.ClassifierContext, ...ai.ModelsClassifierOptions) ai.ClassifierResult
	// GenerateImages is the Session runtime's generateImages; an extension's ctx.modelRegistry.generateImages reaches it. Nil answers an error result.
	GenerateImages func(context.Context, *ai.ImageModel, ai.ImagesContext, ...ai.ModelsImagesOptions) ai.AssistantImages
	Transport      ai.Transport
}

// WireModelOperations installs one Session-owned extension model surface and returns its catalog-listener detach function.
func WireModelOperations(bridge ModelOperationBridge, bindings ModelOperationBindings) func() {
	if bridge == nil {
		return func() {}
	}
	bridge.SetHostAction("getModelInfo", func() map[string]any {
		if bindings.CurrentModel == nil {
			return nil
		}
		return extension.ModelInfo(bindings.CurrentModel())
	})
	bridge.SetHostAction("getModel", func(providerID, modelID string) map[string]any {
		if bindings.ModelLookup == nil {
			return nil
		}
		return extension.ModelInfo(bindings.ModelLookup(providerID, modelID))
	})
	catalogEncoding := &extensionCatalogEncoding{}
	getModels := func() []map[string]any {
		if bindings.ModelCatalog == nil {
			return nil
		}
		models := bindings.ModelCatalog()
		result := make([]map[string]any, 0, len(models))
		for _, model := range models {
			if info := extension.ModelInfo(model); info != nil {
				result = append(result, info)
			}
		}
		return result
	}
	if encoded, ok := bridge.(modelCatalogEncodingBridge); ok {
		encoded.SetModelCatalog(getModels, func() (json.RawMessage, error) {
			if bindings.ModelCatalog == nil {
				return json.RawMessage("null"), nil
			}
			// The first publication has no registry provider state or registration ordering yet. Share only its model encoding; later publications still read current metadata and auth.
			state := catalogEncoding.state(nil, bindings.ModelCatalog())
			if models, ok := state["models"].(json.RawMessage); ok {
				return models, nil
			}
			return json.Marshal(state["models"])
		})
	} else {
		bridge.SetHostAction("getModels", getModels)
	}
	bridge.SetHostAction("getModelAuth", func(ctx context.Context, providerID, modelID string) map[string]any {
		extension.CallInitiated(ctx)
		if bindings.Registry == nil {
			return map[string]any{"ok": false, "error": "model registry not available"}
		}
		if providerID == "test-faux" && os.Getenv("PI_TEST_FAUX") == "1" {
			// The scripted test provider authenticates as the upstream side of
			// the parity harness registers it (test/parity/testdata/test-faux-provider.ts:
			// apiKey "unused"), so extensions take the same authenticated path.
			return map[string]any{"ok": true, "apiKey": "unused", "baseUrl": "http://localhost:0"}
		}
		if bindings.Registry.NativeProvider(providerID) != nil {
			auth, err := bindings.Registry.NativeProviderAuth(ctx, providerID, ai.AuthResolutionOverrides{})
			if err != nil {
				return map[string]any{"ok": false, "error": err.Error()}
			}
			result := map[string]any{"ok": true}
			if auth != nil {
				maps.Copy(result, AuthResultJSON(auth)["auth"].(map[string]any))
				if auth.Env != nil {
					result["env"] = auth.Env
				}
			}
			return result
		}
		var model *ai.Model
		if bindings.ModelLookup != nil {
			model = bindings.ModelLookup(providerID, modelID)
		} else {
			for _, candidate := range bindings.Registry.GetProviderModelData(providerID) {
				if candidate.ID == modelID {
					model = candidate
					break
				}
			}
		}
		if model == nil {
			return map[string]any{"ok": false, "error": "model not found"}
		}
		auth, err := bindings.Registry.ResolveCompatibilityModelAuth(ctx, model)
		if err != nil {
			return map[string]any{"ok": false, "error": err.Error()}
		}
		result := map[string]any{"ok": true}
		maps.Copy(result, AuthResultJSON(auth)["auth"].(map[string]any))
		if auth.Env != nil {
			result["env"] = auth.Env
		}
		return result
	})
	bridge.SetHostAction("getModelRegistryState", func() map[string]any {
		var catalog []*ai.Model
		if bindings.ModelCatalog != nil {
			catalog = bindings.ModelCatalog()
		}
		return catalogEncoding.state(bindings.Registry, catalog)
	})
	bridge.SetHostAction("getAvailableOfType", func(ctx context.Context, modelType, providerID string) ([]map[string]any, error) {
		return extensionAvailableOfType(ctx, bindings.Registry, modelType, providerID)
	})
	bridge.SetHostAction("classify", func(ctx context.Context, model map[string]any, request json.RawMessage, options map[string]any) (json.RawMessage, error) {
		return extensionClassify(ctx, bindings.Classify, model, request, options), nil
	})
	bridge.SetHostAction("generateImages", func(ctx context.Context, model map[string]any, request json.RawMessage, options map[string]any) (json.RawMessage, error) {
		return extensionGenerateImages(ctx, bindings.GenerateImages, model, request, options), nil
	})
	bridge.SetHostAction("getProviderAuth", func(ctx context.Context, providerID string) (map[string]any, error) {
		if bindings.Registry == nil {
			return nil, nil
		}
		result, err := bindings.Registry.ExtensionProviderAuth(ctx, providerID)
		if err != nil {
			return nil, err
		}
		return AuthResultJSON(result), nil
	})
	bridge.SetHostAction("refreshModelRegistry", func(ctx context.Context, allowNetwork *bool, providers []string, force *bool) (map[string]any, error) {
		errors := map[string]string{}
		if bindings.Registry == nil {
			return map[string]any{"aborted": false, "errors": errors}, nil
		}
		result := bindings.Registry.ExtensionRefresh(ctx, allowNetwork, providers, force)
		for providerID, err := range result.Errors {
			errors[providerID] = err.Error()
		}
		return map[string]any{"aborted": result.Aborted, "errors": errors}, nil
	})
	stream := func(ctx context.Context, model map[string]any, request map[string]any) (*ai.AssistantMessageEventStream, error) {
		return streamModelForSubprocess(ctx, model, request, bindings)
	}
	bridge.SetHostAction("streamModel", stream)
	bridge.SetHostAction("complete", func(ctx context.Context, model map[string]any, request map[string]any, _ map[string]any) (map[string]any, error) {
		stream, err := stream(ctx, model, request)
		if err != nil {
			return nil, err
		}
		extension.CallInitiated(ctx)
		encoded, err := json.Marshal(stream.Result())
		if err != nil {
			return nil, err
		}
		var result map[string]any
		if err := json.Unmarshal(encoded, &result); err != nil {
			return nil, err
		}
		return result, nil
	})
	if bindings.Registry == nil {
		return func() {}
	}
	// The typed models are recomposed on the same committed registry changes that republish the catalog. The subscription is an observer: the registry's single listener slot belongs to ModelRuntime.SetChangeListener callers.
	detach := bindings.Registry.ObserveChanges(func() {
		catalogEncoding.invalidateTyped()
		bridge.PublishModelCatalog()
	})
	bridge.PublishModelCatalog()
	return detach
}

func streamModelForSubprocess(ctx context.Context, model map[string]any, request map[string]any, bindings ModelOperationBindings) (*ai.AssistantMessageEventStream, error) {
	if bindings.ModelBuilder == nil {
		return nil, fmt.Errorf("model builder not available")
	}
	if bindings.SessionHandle == nil {
		return nil, fmt.Errorf("session model runtime not available")
	}
	bridgeRequest := extension.ModelStreamRequestFromContext(ctx)
	var llmModel *ai.Model
	var err error
	if bridgeRequest.API {
		llmModel, err = subprocessAPIModel(model)
	} else {
		llmModel, err = bindings.ModelBuilder(subprocessModelSpec(model))
	}
	if err != nil {
		return nil, err
	}
	// A null option is an absent one: upstream providers read undefined and
	// null alike, and extensions pass getApiKeyAndHeaders' null headers and
	// env straight through.
	present := make(map[string]any, len(request))
	for key, value := range request {
		if value != nil {
			present[key] = value
		}
	}
	request = present
	messages, err := subprocessRequestMessages(request["messages"])
	if err != nil {
		return nil, err
	}
	systemPrompt, err := subprocessOptionalString(request, "systemPrompt", "model request")
	if err != nil {
		return nil, err
	}
	var tools []ai.ToolSchema
	if rawTools, exists := request["tools"]; exists {
		if err := subprocessDecodeJSON(rawTools, &tools, "model request.tools"); err != nil {
			return nil, err
		}
	}
	options, err := subprocessStreamOptions(request, ai.StreamOptions{IsReasoning: llmModel.Capabilities.MaxThinking != "", Transport: bindings.Transport})
	if err != nil {
		return nil, err
	}
	maxRetries, err := subprocessOptionalInt64(request, "maxRetries", "model request")
	if err != nil {
		return nil, err
	}
	var maxRetryDelay *int
	if _, exists := request["maxRetryDelayMs"]; exists {
		value, err := subprocessOptionalInt64(request, "maxRetryDelayMs", "model request")
		if err != nil {
			return nil, err
		}
		maxRetryDelay = new(int(value))
	}
	ctx = ai.WithProviderRequestRetry(ctx, int(maxRetries), maxRetryDelay)
	options.Fetch = bridgeRequest.Fetch
	options.OnPayload = bridgeRequest.OnPayload
	options.OnResponse = bridgeRequest.OnResponse
	options.TransformHeaders = bridgeRequest.TransformHeaders
	return bindings.SessionHandle.StreamModel(ctx, llmModel, ai.Context{SystemPrompt: systemPrompt, Messages: messages, Tools: tools}, options), nil
}

func (m *InteractiveMode) streamForSubprocess(ctx context.Context, model map[string]any, request map[string]any) (*ai.AssistantMessageEventStream, error) {
	return streamModelForSubprocess(ctx, model, request, ModelOperationBindings{
		ModelBuilder: m.opts.ModelBuilder, SessionHandle: m.opts.SessionHandle,
		Transport: ai.Transport(m.opts.Settings.Transport),
	})
}

// refreshSessionTools is ctx.refreshTools / pi.refreshTools: the Session admits and declares the tools extensions registered, then this mode rebuilds its tool prompt. An in-process caller may run on the main goroutine, so it does not wait for the rebuild; a subprocess call does.
// upstream: agent-session.ts:3353
func (m *InteractiveMode) refreshSessionTools(wait bool) error {
	session, ok := m.opts.SessionHandle.(interface{ RefreshTools() error })
	if !ok {
		return errors.New("interactive Session cannot refresh tools")
	}
	if wait && m.runCtx != nil {
		return m.runOnMainAndWait(m.runCtx, func() error {
			if err := session.RefreshTools(); err != nil {
				return err
			}
			m.rebuildToolSystemPrompt()
			return nil
		})
	}
	if err := session.RefreshTools(); err != nil {
		return err
	}
	if m.runCtx == nil {
		m.rebuildToolSystemPrompt()
		return nil
	}
	return m.postToMain(m.runCtx, m.rebuildToolSystemPrompt)
}
