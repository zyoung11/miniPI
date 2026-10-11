package ai

import (
	"runtime"

	"pi/internal/coding/identity"
	"pi/internal/coding/piversion"
)

// ProductVersion is pi's composite release version for outbound identifiers.
// It defaults to piversion.Version because importing coding would create a cycle.
var ProductVersion = piversion.Version

// pi divergence (D65): upstream getPiUserAgent
// (packages/ai/src/utils/pi-user-agent.ts) reports "pi (<platform> <release>;
// <arch>)" from Node's os module, or "pi (browser)" with no Node/Bun runtime.
// pi has no browser build and always runs as a native process, so
// PiUserAgent reports the same platform/release/arch shape with pi's own
// product name and composite version instead: "pi/<coding.Version>
// (<platform> <release>; <arch>)" (owner decision, delivery/OWNER-DECISIONS.md
// Q4).
func PiUserAgent() string {
	return identity.UserAgentProduct + "/" + ProductVersion + " (" + piUserAgentPlatform(runtime.GOOS) + " " + piUserAgentRelease() + "; " + piUserAgentArch(runtime.GOARCH) + ")"
}

// piUserAgentPlatform maps runtime.GOOS to the vocabulary Node's
// os.platform() uses, since upstream's user agent reports that value.
func piUserAgentPlatform(goos string) string {
	if goos == "windows" {
		return "win32"
	}
	return goos
}

// piUserAgentArch maps runtime.GOARCH to the vocabulary Node's os.arch()
// uses, since upstream's user agent reports that value.
func piUserAgentArch(goarch string) string {
	switch goarch {
	case "amd64":
		return "x64"
	case "386":
		return "ia32"
	default:
		return goarch
	}
}
