package codingagent

import (
	"os"
)

// experimentalFeaturesEnabled reports whether experimental features are on.
// Upstream core/experimental.ts areExperimentalFeaturesEnabled checks
// process.env.PI_EXPERIMENTAL === "1".
func experimentalFeaturesEnabled() bool {
	return os.Getenv("PI_EXPERIMENTAL") == "1"
}

// AreExperimentalFeaturesEnabled reports whether experimental features are on.
func AreExperimentalFeaturesEnabled() bool { return experimentalFeaturesEnabled() }
