package coding

import icodingagent "pi/internal/codingagent"

// NewInMemorySettingsManager creates independent settings without file I/O. Pass it through ServicesOptions.SettingsManager to use it for Sessions.
// Ports packages/coding-agent/src/core/settings-manager.ts:403-410.
func NewInMemorySettingsManager(settings Settings) *SettingsManager {
	return icodingagent.NewInMemorySettingsManager(settings)
}
