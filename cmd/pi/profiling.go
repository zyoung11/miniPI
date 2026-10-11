// SPDX-FileCopyrightText: Copyright Hewlett Packard Enterprise Development LP
// SPDX-License-Identifier: MIT

package main

import (
	"os"

	"pi/internal/codingagent"
)

// stopProfiles writes the PI_PROFILE profiles that main started. It is a no-op
// when PI_PROFILE is unset.
var stopProfiles = func() {}

// stopModelServices cancels and drains Services-owned model work while extension transports are still available.
var stopModelServices = func() {}

// exitProcess releases startup extension ownership and writes profiles before
// exiting. os.Exit skips deferred calls, so main uses this on every exit path.
func exitProcess(code int) {
	stopAutomaticExtensionCacheGC()
	stopModelServices()
	stopStartupExtensions()
	codingagent.RestoreStdout()
	stopProfiles()
	// pi additive (D102): every exit through here is requested or prints its reason, so the session marker goes.
	codingagent.EndSessionMarker()
	os.Exit(code)
}
