// UpstreamVersion and UpstreamCommit are the exact upstream Pi pins. The CLI,
// parity runner, source mirror, generated inventories, and pi Porter consume
// them. Change both together, in internal/coding/pigversion/piversion.go, then run
// the upgrade workflow in the porter skill.
package coding

import "pi/internal/coding/piversion"

// Version is pi's displayed release line.
const Version = piversion.Version

// UpstreamCommit is the exact Pi release commit mirrored for UpstreamVersion.
const UpstreamCommit = piversion.UpstreamCommit

// UpstreamVersion is the Pi release whose behavior pi targets.
const UpstreamVersion = piversion.UpstreamVersion

// UpstreamReviewedVersion is the most recent pin whose parity ledgers carry
// reviewed dispositions. Leap ledgers (the semantic interface delta, the
// behavior-input mapping carry-forward, and the upstream-sync manifest) cover
// every change from this version to UpstreamVersion. Advance it only after
// those ledgers have no pending rows.
const UpstreamReviewedVersion = "0.84.0"
