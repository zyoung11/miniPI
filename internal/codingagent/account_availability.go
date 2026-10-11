package codingagent

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"pi/ai"
)

// Account availability is the set of model ids a provider reports as usable by
// the signed-in account. A provider whose credential is refused snapshots an
// empty set (its catalog hides); a provider the probe cannot reach keeps no
// snapshot and its catalog lists everything (the probe never removes what it
// could not check).

// accountProbeTimeout bounds one provider's availability probe.
const accountProbeTimeout = 5 * time.Second

// accountAvailability holds per-provider reachability verdicts. A provider is
// usable when its listing endpoint answers with the credential; the model list
// itself is not authoritative (a relay or custom deployment serves models the
// public listing omits), so the verdict is provider-level.
type accountAvailability struct {
	mu        sync.RWMutex
	snapshots map[string]bool
}

func newAccountAvailability() *accountAvailability {
	return &accountAvailability{snapshots: map[string]bool{}}
}

// snapshot returns the provider's verdict and whether the probe judged it.
func (a *accountAvailability) snapshot(providerID string) (bool, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	usable, known := a.snapshots[providerID]
	return usable, known
}

// set records one provider's verdict.
func (a *accountAvailability) set(providerID string, usable bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.snapshots[providerID] = usable
}

// RefreshAccountAvailability probes every authenticated provider's model
// listing and records which models the account may use. Providers whose
// credential is refused hide their catalog; unreachable providers keep
// listing everything.
func (r *ModelRegistry) RefreshAccountAvailability(ctx context.Context) {
	var wg sync.WaitGroup
	for _, id := range r.modelProviderIDs() {
		if !r.HasConfiguredAuth(id) {
			continue
		}
		models := r.GetProviderModelData(id)
		if len(models) == 0 {
			continue
		}
		model := models[0]
		wg.Go(func() {
			usable, judged := probeAccountModels(ctx, model, r)
			if !judged {
				return
			}
			r.account.set(id, usable)
		})
	}
	wg.Wait()
}

// probeAccountModels queries the provider's model listing and reports whether
// the account can reach it with its credential. A refused credential or an
// unreachable endpoint judges the provider unusable; it reports false only for
// server-side trouble, which leaves the provider unfiltered.
func probeAccountModels(ctx context.Context, model *ai.Model, r *ModelRegistry) (bool, bool) {
	probeCtx, cancel := context.WithTimeout(ctx, accountProbeTimeout)
	defer cancel()
	resolution, err := r.ResolveRegistryModelAuth(probeCtx, model)
	if err != nil || resolution == nil {
		return false, true
	}
	request, err := http.NewRequestWithContext(probeCtx, http.MethodGet, accountModelsURL(resolution.Auth.BaseURL, model.ProviderMeta.BaseURL), nil)
	if err != nil {
		return false, true
	}
	for key, value := range resolution.Auth.Headers {
		if value == nil {
			request.Header.Del(key)
			continue
		}
		request.Header.Set(key, *value)
	}
	if request.Header.Get("Authorization") == "" && resolution.Auth.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+resolution.Auth.APIKey)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return false, true
	}
	defer response.Body.Close()
	switch {
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		return false, true
	case response.StatusCode != http.StatusOK:
		// Any other answer (missing endpoint, gateway hiccup) says nothing
		// about the account: the provider stays listed.
		return false, false
	}
	return true, true
}

// accountModelsURL builds the model listing URL: the resolved request base,
// falling back to the catalog's base, with /models appended at the API root.
func accountModelsURL(resolved, fallback string) string {
	base := strings.TrimRight(resolved, "/")
	if base == "" {
		base = strings.TrimRight(fallback, "/")
	}
	base = strings.TrimSuffix(base, "/v1")
	return base + "/v1/models"
}

// AccountModelAvailable reports whether one model survives the account probe:
// models of a provider the probe could not judge always qualify.
func (r *ModelRegistry) AccountModelAvailable(providerID, modelID string) bool {
	return r.accountModelFilter(providerID)(modelID)
}
