package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"pi/ai"
	"pi/coding"
)

const rpcUsageProbeDir = "testdata/d82-w7"

// rpcUsageBody is the body rpc-usage-probe.mjs serves: every SSE chunk carries a growing usage, and all of it arrives in one write.
func rpcUsageBody() string {
	chunk := func(content string, output int, finish string) string {
		delta := "{}"
		if content != "" {
			delta = fmt.Sprintf(`{"content":%q}`, content)
		}
		reason := "null"
		if finish != "" {
			reason = fmt.Sprintf("%q", finish)
		}
		return fmt.Sprintf("data: {\"id\":\"chatcmpl-w7\",\"model\":\"strict\",\"choices\":[{\"index\":0,\"delta\":%s,\"finish_reason\":%s}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":%d,\"total_tokens\":%d}}\n\n", delta, reason, output, 10+output)
	}
	return chunk("a", 1, "") + chunk("b", 2, "") + chunk("c", 3, "") + chunk("d", 4, "") + chunk("e", 5, "stop") + "data: [DONE]\n\n"
}

// rpcUsageFrames reduces an RPC frame the way rpc-usage-probe.mjs does.
func rpcUsageFrames(t *testing.T, frames []any) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, frame := range frames {
		raw, err := json.Marshal(frame)
		if err != nil {
			t.Fatal(err)
		}
		var event struct {
			Type    string                `json:"type"`
			Usage   *struct{ Output int } `json:"usage"`
			Message *struct {
				Role       string                `json:"role"`
				Content    json.RawMessage       `json:"content"`
				Usage      *struct{ Output int } `json:"usage"`
				StopReason string                `json:"stopReason"`
			} `json:"message"`
			Update *struct {
				Type  string `json:"type"`
				Delta string `json:"delta"`
			} `json:"assistantMessageEvent"`
		}
		if err := json.Unmarshal(raw, &event); err != nil {
			t.Fatal(err)
		}
		entry := map[string]any{"type": event.Type}
		delta := ""
		if event.Update != nil && (event.Update.Type == "text_delta" || event.Type == "message_update") {
			delta = event.Update.Delta
		}
		switch {
		case event.Message != nil && event.Message.Role == "assistant" && (event.Type == "message_start" || event.Type == "message_update" || event.Type == "message_end"):
			usage := event.Usage
			if usage == nil {
				usage = event.Message.Usage
			}
			var blocks []struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(event.Message.Content, &blocks); err != nil {
				t.Fatal(err)
			}
			var text strings.Builder
			for _, block := range blocks {
				text.WriteString(block.Text)
			}
			if delta != "" {
				entry["delta"] = delta
			}
			entry["outputTokens"] = float64(usage.Output)
			if event.Message.StopReason != "" {
				entry["stopReason"] = event.Message.StopReason
			}
			entry["text"] = text.String()
		case event.Type == "message_update" && event.Usage != nil:
			if delta != "" {
				entry["delta"] = delta
			}
			entry["outputTokens"] = float64(event.Usage.Output)
		default:
			continue
		}
		out = append(out, entry)
	}
	return out
}

// rpcUsageServer serves rpcUsageBody in one write, as the probe's loopback server does.
func rpcUsageServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		body := rpcUsageBody()
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		_, _ = io.WriteString(w, body)
	}))
}

// rpcUsageModel builds the model the real CLI builds, through Model Runtime and its forwarding layers, which set the start state.
func rpcUsageModel(t *testing.T, server *httptest.Server) (*coding.Services, *ai.Model) {
	t.Helper()
	// The real CLI reaches the provider through Model Runtime and its forwarding layers, which set the start state; use the same path.
	agentDir := t.TempDir()
	config := fmt.Sprintf(`{"providers":{"p":{"baseUrl":%q,"api":"openai-completions","apiKey":"k","models":[{"id":"strict","name":"strict"}]}}}`, server.URL+"/v1")
	if err := os.WriteFile(filepath.Join(agentDir, "models.json"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	services, err := coding.NewServices(coding.ServicesOptions{CWD: t.TempDir(), AgentDir: agentDir})
	if err != nil {
		t.Fatal(err)
	}
	model, err := coding.BuildModel("p/strict", services)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = model.Provider.Close() })
	return services, model
}

func readRPCUsageGolden(t *testing.T) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(rpcUsageProbeDir, "rpc-usage-pi.json"))
	if err != nil {
		t.Fatal(err)
	}
	var golden []map[string]any
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	return golden
}

// The goldens are real Pi 0.99.1 output: --mode rpc without an extension, and --mode rpc with coding/testdata/d82-w7/message-start-wait-extension.mjs, whose message_start handler awaits a timer (the extension golden's first frame shows the content streamed during that await; TestSessionNodeExtensionAwaitLetsProviderAdvance asserts it through pi's Session). This proves they are not stale.
// rpc-mode.ts:354-363 serializes each event in the Session subscriber and then, in an Agent listener registered after the Session's, awaits waitForRawStdoutBackpressure (output-guard.ts:95-101). The write callback runs after every promise reaction, so the provider finishes its buffered body before the next event is copied and serialized. Real Pi shows the final usage on the first message_update.
// print-mode.ts:108-118 gives JSON mode the same Session subscriber and Agent backpressure listener as RPC. Real Pi's --mode json output for the same body is the golden.
func TestJSONModeStdoutBackpressureMatchesPi(t *testing.T) {
	want := readRPCUsageGolden(t)
	for run := range 5 {
		server := rpcUsageServer(t)
		services, model := rpcUsageModel(t, server)
		host := printModeRuntime{Services: services, Session: coding.SessionStartOptions{Model: model, SkipBuiltinTools: true, SessionDir: t.TempDir()}}
		result := runPrintModeForTest(t, host, printModeOptions{Mode: "json", InitialMessage: "hi"})
		server.Close()
		if result.err != nil || result.stderr != "" {
			t.Fatalf("run %d: %v, stderr=%q", run, result.err, result.stderr)
		}
		var frames []any
		for line := range strings.SplitSeq(strings.TrimSpace(result.stdout), "\n") {
			frames = append(frames, json.RawMessage(line))
		}
		if got := rpcUsageFrames(t, frames); !reflect.DeepEqual(got, want) {
			t.Fatalf("run %d frames differ from Pi\n got: %v\nwant: %v", run, got, want)
		}
	}
}
