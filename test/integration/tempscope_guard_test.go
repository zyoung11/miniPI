//go:build integration

package integration

import (
	"testing"

	"pi/internal/testenv"
)

// Guards the TestMain temporary-directory scope: pi and tmux children write scratch files under the run's directory, which TestMain removes.
func TestTempDirIsScopedToTheIntegrationRun(t *testing.T) {
	testenv.RequireScopedTempDir(t, "pi-integration-")
}
