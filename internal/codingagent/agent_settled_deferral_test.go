package codingagent

import (
	"reflect"
	"testing"

	"pi/coding/extension"
	"pi/coding/extension/host/inproc"
)

func TestAgentSettledDefersRunStartingActionsUntilAllHandlersReturn(t *testing.T) {
	var lifecycle []string
	mode := &InteractiveMode{}
	ext := extension.Extension{Path: "settled", Handlers: map[string][]extension.HandlerFn{
		EventAgentSettled: {
			func(...any) (any, error) {
				lifecycle = append(lifecycle, "first")
				start := func() { lifecycle = append(lifecycle, "start") }
				if !mode.deferSettledAction(start) {
					start()
				}
				return nil, nil
			},
			func(...any) (any, error) {
				lifecycle = append(lifecycle, "second")
				return nil, nil
			},
		},
	}}
	mode.newRunner = inproc.NewRunner([]extension.Extension{ext}, t.TempDir())
	mode.emitAgentSettledEvent()
	if want := []string{"first", "second", "start"}; !reflect.DeepEqual(lifecycle, want) {
		t.Fatalf("lifecycle = %v, want %v", lifecycle, want)
	}
}
