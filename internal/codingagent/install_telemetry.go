package codingagent

// install_telemetry.go ports upstream reportInstallTelemetry and its two call
// sites in getChangelogForDisplay
// (packages/coding-agent/src/modes/interactive/interactive-mode.ts:1265-1307),
// gated by core/telemetry.ts's isInstallTelemetryEnabled
// (internal/codingagent/settings.go: SettingsManager.IsInstallTelemetryEnabled).

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"pi/ai"
)

// pi divergence (D64): pi reports installs/updates to its own pi-in-go.dev
// endpoint instead of Pi's pi.dev.
const defaultInstallTelemetryURL = "https://pi-in-go.dev/api/report-install"

// installTelemetryTimeout bounds the outbound request at upstream's own
// value.
// upstream: interactive-mode.ts:reportInstallTelemetry (AbortSignal.timeout(5000))
const installTelemetryTimeout = 5 * time.Second

// installTelemetryURL returns the configured report-install endpoint.
// PI_INSTALL_TELEMETRY_URL overrides it; tests use this to point at a local
// httptest server instead of the network, and it doubles as the general
// injectable-endpoint mechanism (there is no upstream equivalent: Pi hardcodes
// pi.dev).
func installTelemetryURL() string {
	if configured := strings.TrimSpace(os.Getenv("PI_INSTALL_TELEMETRY_URL")); configured != "" {
		return configured
	}
	return defaultInstallTelemetryURL
}

// installTelemetryAllowed reports whether reportInstallTelemetry may send its
// ping. Mirrors upstream's two guards (interactive-mode.ts:1293-1296):
//
//   - `if (process.env.PI_OFFLINE) return;` — JS truthiness of a string env
//     var: any non-empty string is truthy, including a whitespace-only one
//     (JS never trims it). The raw, untrimmed value is checked here for the
//     same reason: `PI_OFFLINE=" "` must suppress telemetry, exactly as it
//     does upstream, so this must not use strings.TrimSpace.
//   - `isInstallTelemetryEnabled(this.settingsManager)`
//     (SettingsManager.IsInstallTelemetryEnabled, honoring PI_TELEMETRY).
func installTelemetryAllowed(sm *SettingsManager) bool {
	if os.Getenv("PI_OFFLINE") != "" {
		return false
	}
	return sm != nil && sm.IsInstallTelemetryEnabled()
}

// reportInstallTelemetry sends pi's anonymous install/update ping: the
// version alone, as a `version` query parameter, to pi's report-install
// endpoint. It mirrors upstream reportInstallTelemetry (interactive-mode.ts:
// 1292-1307): skip entirely when installTelemetryAllowed says no, and
// otherwise fire an unawaited, best-effort HTTPS GET with a 5s timeout whose
// result and error are both discarded. It never blocks the caller and never
// surfaces a user-visible error, matching upstream's fire-and-forget
// `void fetch(...).then().catch()`. The gate check and the version-empty
// check both run synchronously, before the goroutine is spawned, so a caller
// that observes no request immediately after this returns has proven none
// will ever be sent for this call.
func reportInstallTelemetry(sm *SettingsManager, version string) {
	if !installTelemetryAllowed(sm) {
		return
	}
	version = strings.TrimSpace(version)
	if version == "" {
		return
	}
	endpoint := installTelemetryURL()
	go sendInstallTelemetry(endpoint, version)
}

// buildInstallTelemetryRequestURL returns endpoint with its query replaced by
// exactly one `version` parameter. Upstream sends only
// `?version=<version>` (interactive-mode.ts:1300); any other query
// parameters already present on the configured endpoint (for example ones
// baked into a PI_INSTALL_TELEMETRY_URL override) must not leak onto the
// wire, so the query is rebuilt from scratch rather than mutated in place.
func buildInstallTelemetryRequestURL(endpoint, version string) (string, error) {
	target, err := url.Parse(endpoint)
	if err != nil || target.Scheme == "" || target.Host == "" {
		return "", fmt.Errorf("invalid install telemetry endpoint %q", endpoint)
	}
	target.RawQuery = url.Values{"version": {version}}.Encode()
	return target.String(), nil
}

// sendInstallTelemetry performs the actual request. It runs in its own
// goroutine (upstream's unawaited fetch) and swallows every error: a network
// failure, timeout, or non-2xx response must never affect the interactive
// session. No data beyond the version and the standard pi User-Agent is
// sent, matching upstream, which sends only `?version=<version>` and the
// User-Agent header.
func sendInstallTelemetry(endpoint, version string) {
	requestURL, err := buildInstallTelemetryRequestURL(endpoint, version)
	if err != nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), installTelemetryTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return
	}
	req.Header.Set("User-Agent", ai.PiUserAgent())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	_ = resp.Body.Close()
}

