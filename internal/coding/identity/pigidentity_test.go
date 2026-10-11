package identity

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// The owner-approved pi values (docs/parity/DIVERGENCES.md D26, D64, D65).
// They are spelled out here so a change to identity.json cannot pass by
// rewriting both sides.
func TestIdentityValuesAreThePiGBrand(t *testing.T) {
	want := map[string]string{
		"OpenRouterReferer":    "https://pi",
		"OpenRouterTitle":      "Pi",
		"OpenRouterCategories": "cli-agent",
		"NvidiaBillingOrigin":  "Pi",
		"CloudflareUserAgent":  "pi-coding-agent",
		"OpenCodeClient":       "pi",
		"CodexOriginator":      "pi",
		"XAIReferrer":          "pi",
		"AgentMarker":          "pi",
		"UserAgentProduct":     "pi",
		"HostedOrigin":         "https://pi-in-go.dev",
	}
	got := map[string]string{
		"OpenRouterReferer":    OpenRouterReferer,
		"OpenRouterTitle":      OpenRouterTitle,
		"OpenRouterCategories": OpenRouterCategories,
		"NvidiaBillingOrigin":  NvidiaBillingOrigin,
		"CloudflareUserAgent":  CloudflareUserAgent,
		"OpenCodeClient":       OpenCodeClient,
		"CodexOriginator":      CodexOriginator,
		"XAIReferrer":          XAIReferrer,
		"AgentMarker":          AgentMarker,
		"UserAgentProduct":     UserAgentProduct,
		"HostedOrigin":         HostedOrigin,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("identity = %v, want %v", got, want)
	}
}

// identity.json is the one source of these values for Go and for the Node
// vendoring step (automation/gen/pi-identity-patches.mjs reads the same file).
func TestIdentityJSONIsTheSingleSource(t *testing.T) {
	data, err := os.ReadFile("identity.json")
	if err != nil {
		t.Fatal(err)
	}
	var file map[string]string
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"openRouterReferer":    OpenRouterReferer,
		"openRouterTitle":      OpenRouterTitle,
		"openRouterCategories": OpenRouterCategories,
		"nvidiaBillingOrigin":  NvidiaBillingOrigin,
		"cloudflareUserAgent":  CloudflareUserAgent,
		"openCodeClient":       OpenCodeClient,
		"codexOriginator":      CodexOriginator,
		"xaiReferrer":          XAIReferrer,
		"agentMarker":          AgentMarker,
		"userAgentProduct":     UserAgentProduct,
		"hostedOrigin":         HostedOrigin,
	}
	if !reflect.DeepEqual(file, want) {
		t.Fatalf("identity.json = %v, want the exported values %v", file, want)
	}
}
