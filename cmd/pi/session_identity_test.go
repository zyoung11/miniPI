package main

import (
	"encoding/json"
	"strings"
	"testing"

	"pi/coding"
	"pi/coding/extension/host/subprocess"

	"github.com/google/uuid"
)

// Pi's SessionManager.newSession creates the header/id before choosing a file;
// inMemory changes persistence, not identity (session-manager.ts:1057-1087,1801-1802).
func TestSessionReadBindingsKeepIdentityWithoutPersistence(t *testing.T) {
	for _, noSession := range []bool{false, true} {
		t.Run(map[bool]string{false: "persisted", true: "memory"}[noSession], func(t *testing.T) {
			services, err := coding.NewServices(coding.ServicesOptions{CWD: t.TempDir(), AgentDir: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			var current *coding.Session
			dir := t.TempDir()
			bridge := subprocess.NewUIBridge(func() {})
			bindSessionReadActions(bridge, func() *coding.Session { return current }, services.CWD(), dir)
			var previous string
			for range 2 {
				current, err = coding.NewSession(services, coding.SessionOptions{NoSession: noSession, SessionDir: dir})
				if err != nil {
					t.Fatal(err)
				}
				session := current
				t.Cleanup(func() { _ = session.Close() })
				id := current.ID()
				requireSessionUUIDv7(t, id)
				if id == previous || current.Inner().Header().ID != id {
					t.Fatalf("session/header identity = %q, previous %q", id, previous)
				}
				previous = id
				if state := bridge.Snapshot(nil, 0, false); state.Session == nil || state.Session.SessionID != id || state.Session.SessionFile != current.Path() {
					t.Fatalf("Node state = %+v, want %s", state.Session, id)
				}
				for method, args := range map[string]json.RawMessage{
					"getSessionID": nil,
					"sessionRead":  json.RawMessage(`{"method":"getSessionId"}`),
				} {
					result, err := bridge.HandleCall("probe", &subprocess.CallPayload{Method: method, Args: args})
					if err != nil || result.Error != nil || !strings.Contains(string(result.Result), `"`+id+`"`) {
						t.Fatalf("%s = %+v, %v, want %s", method, result, err, id)
					}
				}
				if noSession && current.Path() != "" {
					t.Fatalf("in-memory session has path %q", current.Path())
				}
				if err := current.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func requireSessionUUIDv7(t *testing.T, id string) {
	t.Helper()
	parsed, err := uuid.Parse(id)
	if err != nil || parsed.Version() != 7 || parsed.Variant() != uuid.RFC4122 || parsed.String() != id {
		t.Fatalf("session id %q is not a canonical UUIDv7: %v", id, err)
	}
}

type sessionIdentityRecord struct {
	Event     string `json:"event"`
	ID        string `json:"id"`
	Persisted bool   `json:"persisted"`
	File      string `json:"file"`
	Header    struct {
		ID string `json:"id"`
	} `json:"header"`
}

// The real CLI must bind the current SessionManager before dispatching lifecycle
// handlers and tools in print/JSON/RPC, including --no-session (Pi main.ts:363-447).
