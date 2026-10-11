// Ports packages/coding-agent/src/core/chat-template-thinking.ts: llama.cpp
// chat-template thinking detection. The server's own template render is the
// source of truth: /apply-template answers with the prompt the template
// produced and runs no inference, so a refused level fails at render time and
// names the value it will not take, while two levels that render the same
// prompt are aliases of one behaviour.
package llama

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// A llama.cpp server answers /props in milliseconds; an unreachable server must
// not delay a refresh.
const chatTemplateProbeTimeout = 1 * time.Second

// One one-token completion is small, but a busy server queues it behind its own
// work.
const levelProbeTimeout = 10 * time.Second

// Thinking levels a template can accept, in ladder order.
var thinkingLevels = []string{"minimal", "low", "medium", "high", "xhigh", "max"}

// Per-server level verdicts, so a catalog refresh never repeats the probe
// requests.
var thinkingLevelProbeCache sync.Map

// chatTemplateProbe is the verdict for one model: the template toggles
// thinking and may also accept reasoning_effort.
type chatTemplateProbe struct {
	thinking bool
	effort   bool
}

// thinkingLevelRender is the outcome of one level render: the template took
// it, refused it, or the probe could not tell.
type thinkingLevelRender struct {
	verdict   string
	prompt    string
	responded bool
}

// serverRoot returns the server root for a provider base URL: the
// OpenAI-compatible API carries a /v1 suffix and trailing slashes, while the
// management endpoints sit at the root.
func serverRoot(baseURL string) string {
	root := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	root = strings.TrimSuffix(root, "/v1")
	return root
}

// ChatTemplatePropsURL builds the /props URL for one model. `autoload=false`
// keeps the query from loading a router preset or waking a sleeping model.
func ChatTemplatePropsURL(baseURL, modelID string) string {
	return serverRoot(baseURL) + "/props?model=" + url.QueryEscape(modelID) + "&autoload=false"
}

// applyTemplateURL is the render endpoint: it answers with the prompt a
// template produced and runs no inference.
func applyTemplateURL(baseURL string) string {
	return serverRoot(baseURL) + "/apply-template"
}

// chatCompletionURL is the completions endpoint: template render happens
// before generation.
func chatCompletionURL(baseURL string) string {
	return serverRoot(baseURL) + "/v1/chat/completions"
}

// lookupString reads one string field out of a decoded JSON object.
func lookupString(value map[string]any, key string) string {
	if found, ok := value[key].(string); ok {
		return found
	}
	return ""
}

// effortRejectionWords are the error texts that name the effort value the
// template refused.
var effortRejectionWords = []string{
	"unexpected",
	"supported types",
	"not supported",
	"unsupported",
	"must be",
	"unknown effort",
	"expected effort",
}

// rejectsEffortValue reports whether a rejection names both the effort field
// and the bad value, which a transport, schema, or capacity error does not.
// Anything less stays unjudged, so an unrelated failure cannot hide a level
// the template really accepts.
func rejectsEffortValue(body string) bool {
	lower := strings.ToLower(body)
	if !strings.Contains(lower, "reasoning effort") && !strings.Contains(lower, "reasoning_effort") {
		return false
	}
	for _, word := range effortRejectionWords {
		if strings.Contains(lower, word) {
			return true
		}
	}
	return false
}

// probeChatTemplate queries one model's chat template and template
// capabilities; any failure leaves the model unclassified.
func probeChatTemplate(ctx context.Context, baseURL, apiKey, modelID string) chatTemplateProbe {
	failure := chatTemplateProbe{}
	ctx, cancel := context.WithTimeout(ctx, chatTemplateProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ChatTemplatePropsURL(baseURL, modelID), nil)
	if err != nil {
		return failure
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return failure
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return failure
	}
	var payload map[string]any
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return failure
	}
	template := lookupString(payload, "chat_template")
	if template == "" || !strings.Contains(template, "enable_thinking") {
		return failure
	}
	effort := false
	if caps, ok := payload["chat_template_caps"].(map[string]any); ok {
		effort, _ = caps["supports_reasoning_effort"].(bool)
	}
	return chatTemplateProbe{thinking: true, effort: effort}
}

// renderThinkingLevel renders one thinking level through the server's chat
// template. It returns the rendered prompt when the template took the level, a
// rejection when it refused, unknown otherwise.
func renderThinkingLevel(ctx context.Context, baseURL, apiKey, modelID, level string) thinkingLevelRender {
	failure := thinkingLevelRender{verdict: "unknown"}
	ctx, cancel := context.WithTimeout(ctx, levelProbeTimeout)
	defer cancel()
	body, err := json.Marshal(map[string]any{
		"model":                modelID,
		"messages":             []map[string]any{{"role": "user", "content": "hi"}},
		"reasoning_effort":     level,
		"chat_template_kwargs": map[string]any{"enable_thinking": true, "preserve_thinking": true},
	})
	if err != nil {
		return failure
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, applyTemplateURL(baseURL), bytes.NewReader(body))
	if err != nil {
		return failure
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return failure
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	if response.StatusCode == http.StatusOK {
		var payload map[string]any
		if err := json.Unmarshal(raw, &payload); err != nil {
			return thinkingLevelRender{verdict: "unknown", responded: true}
		}
		prompt := lookupString(payload, "prompt")
		if prompt == "" {
			return thinkingLevelRender{verdict: "unknown", responded: true}
		}
		return thinkingLevelRender{verdict: "accepted", prompt: prompt, responded: true}
	}
	if rejectsEffortValue(string(raw)) {
		return thinkingLevelRender{verdict: "rejected", responded: true}
	}
	return thinkingLevelRender{verdict: "unknown", responded: true}
}

// probeThinkingLevelViaCompletion sends one one-token completion for a single
// thinking level. It returns "accepted" when the template renders, "rejected"
// when it refuses the level, "unknown" otherwise.
func probeThinkingLevelViaCompletion(ctx context.Context, baseURL, apiKey, modelID, level string) string {
	ctx, cancel := context.WithTimeout(ctx, levelProbeTimeout)
	defer cancel()
	body, err := json.Marshal(map[string]any{
		"model":                modelID,
		"messages":             []map[string]any{{"role": "user", "content": "hi"}},
		"max_tokens":           1,
		"stream":               false,
		"reasoning_effort":     level,
		"chat_template_kwargs": map[string]any{"enable_thinking": true, "preserve_thinking": true},
	})
	if err != nil {
		return "unknown"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, chatCompletionURL(baseURL), bytes.NewReader(body))
	if err != nil {
		return "unknown"
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return "unknown"
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusOK {
		return "accepted"
	}
	raw, _ := io.ReadAll(response.Body)
	if rejectsEffortValue(string(raw)) {
		return "rejected"
	}
	return "unknown"
}

// isWordChar reports whether char is a word character for whole-word level
// matching.
func isWordChar(r byte) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_'
}

// levelNamedInPrompt reports whether the prompt names the level as a whole
// word, so `high` does not match inside `xhigh`.
func levelNamedInPrompt(prompt, level string) bool {
	index := strings.Index(prompt, level)
	for index != -1 {
		before := byte(' ')
		if index > 0 {
			before = prompt[index-1]
		}
		after := byte(' ')
		if index+len(level) < len(prompt) {
			after = prompt[index+len(level)]
		}
		if !isWordChar(before) && !isWordChar(after) {
			return true
		}
		index = strings.Index(prompt[index+1:], level)
		if index != -1 {
			index += 1
		}
	}
	return false
}

// levelNamedInAnyPrompt returns the level name the prompt carries, preferring
// the longest one when several match.
func levelNamedInAnyPrompt(prompt string, levels []string) string {
	found := ""
	for _, level := range levels {
		if !levelNamedInPrompt(prompt, level) {
			continue
		}
		if len(level) > len(found) {
			found = level
		}
	}
	if found == "" {
		return ""
	}
	return found
}

// levelMapFromRenders builds the map from rendered prompts. Refused levels are
// hidden, and levels whose prompts come out identical are one behaviour under
// two names, so every alias is hidden behind the name the template itself
// resolved to.
func levelMapFromRenders(renders []thinkingLevelRender) map[string]any {
	judged := 0
	for _, render := range renders {
		if render.verdict != "unknown" {
			judged++
		}
	}
	if judged == 0 {
		return nil
	}
	result := map[string]any{}
	for index, render := range renders {
		if render.verdict == "unknown" {
			continue
		}
		if render.verdict == "accepted" {
			result[thinkingLevels[index]] = thinkingLevels[index]
		} else {
			result[thinkingLevels[index]] = nil
		}
	}
	for _, render := range renders {
		if render.verdict != "accepted" {
			continue
		}
		var samePrompt []string
		for other, candidate := range renders {
			if candidate.verdict != "accepted" || candidate.prompt != render.prompt {
				continue
			}
			samePrompt = append(samePrompt, thinkingLevels[other])
		}
		if len(samePrompt) < 2 {
			continue
		}
		named := levelNamedInAnyPrompt(render.prompt, samePrompt)
		representative := named
		if representative == "" {
			representative = samePrompt[len(samePrompt)-1]
		}
		result[representative] = representative
		for _, name := range samePrompt {
			if name == representative {
				continue
			}
			result[name] = nil
		}
	}
	return result
}

// levelMapFromCompletions falls back to one one-token completion per level for
// a server without /apply-template. The template still fails at render time
// and names the value it will not take, but without the rendered prompt two
// aliases of one level cannot be told apart and both stay visible.
func levelMapFromCompletions(ctx context.Context, baseURL, apiKey, modelID string) map[string]any {
	results := make([]string, len(thinkingLevels))
	var wg sync.WaitGroup
	for index, level := range thinkingLevels {
		wg.Add(1)
		go func(index int, level string) {
			defer wg.Done()
			results[index] = probeThinkingLevelViaCompletion(ctx, baseURL, apiKey, modelID, level)
		}(index, level)
	}
	wg.Wait()
	result := map[string]any{}
	judged := 0
	for index, verdict := range results {
		if verdict == "unknown" {
			continue
		}
		judged++
		if verdict == "accepted" {
			result[thinkingLevels[index]] = thinkingLevels[index]
		} else {
			result[thinkingLevels[index]] = nil
		}
	}
	if judged == 0 {
		return nil
	}
	return result
}

// serverResponded reports whether the server answered at all, which is what
// makes the completion fallback worth its requests.
func serverResponded(renders []thinkingLevelRender) bool {
	for _, render := range renders {
		if render.responded {
			return true
		}
	}
	return false
}

// ProbeThinkingLevels probes which thinking levels a loaded model's chat
// template accepts, and which of them are one level under two names. The
// result is cached per server and model, so the probes run once per process
// instead of on every catalog refresh. It returns thinkingLevelMap entries for
// the model, or nil when nothing was learned.
func ProbeThinkingLevels(ctx context.Context, baseURL, apiKey, modelID string) map[string]any {
	cacheKey := serverRoot(baseURL) + "|" + modelID
	if cached, ok := thinkingLevelProbeCache.Load(cacheKey); ok {
		if typed, ok := cached.(map[string]any); ok {
			return typed
		}
		return nil
	}
	renders := make([]thinkingLevelRender, len(thinkingLevels))
	var wg sync.WaitGroup
	for index, level := range thinkingLevels {
		wg.Add(1)
		go func(index int, level string) {
			defer wg.Done()
			renders[index] = renderThinkingLevel(ctx, baseURL, apiKey, modelID, level)
		}(index, level)
	}
	wg.Wait()
	result := levelMapFromRenders(renders)
	if result == nil && serverResponded(renders) {
		result = levelMapFromCompletions(ctx, baseURL, apiKey, modelID)
	}
	if result == nil {
		return nil
	}
	thinkingLevelProbeCache.Store(cacheKey, result)
	return result
}

// DetectChatTemplateThinking probes one model's /props and reports whether its
// chat template toggles thinking through enable_thinking and whether it also
// accepts reasoning_effort.
func DetectChatTemplateThinking(ctx context.Context, baseURL, apiKey, modelID string) (thinking bool, effort bool) {
	probe := probeChatTemplate(ctx, baseURL, apiKey, modelID)
	return probe.thinking, probe.effort
}

// ThinkingLevelMapFromProbe converts a probe map into the model's
// thinkingLevelMap: a level the template took maps to itself, a refused or
// aliased level maps to null, and an unjudged level stays absent.
func ThinkingLevelMapFromProbe(probe map[string]any) *ThinkingLevelMap {
	if probe == nil {
		return nil
	}
	entry := func(level string) *string {
		value, judged := probe[level]
		if !judged {
			return nil
		}
		if value == nil {
			return nil
		}
		if name, ok := value.(string); ok {
			return &name
		}
		return nil
	}
	return &ThinkingLevelMap{
		Minimal: entry("minimal"),
		Low:     entry("low"),
		Medium:  entry("medium"),
		High:    entry("high"),
		XHigh:   entry("xhigh"),
	}
}
