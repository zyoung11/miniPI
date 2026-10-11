package codingagent

import (
	"testing"

	"pi/internal/testenv"
)

// A lane exports PI_CODING_AGENT_DIR for the pi that runs `go test`. Tests here build default session, trust and credential paths through AgentDir, so TestMain must replace the variable before any test runs (TestMaskedLoginErrorDoesNotEnterFramesOrSession wrote a session directory into the exported directory before it did).
func TestPackageTestsDoNotReachTheExportedAgentDirectory(t *testing.T) {
	testenv.RequireIsolatedAgentEnv(t)
}
