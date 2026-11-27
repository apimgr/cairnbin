package main

import (
	"strconv"
	"time"
)

// Version, CommitID, BuildEpoch, and OfficialSite are injected at build time
// through ldflags. BuildDate is derived from BuildEpoch at process startup.
var (
	Version      = "devel"
	CommitID     = "N/A"
	BuildDate    = "N/A"
	BuildEpoch   = "0"
	OfficialSite = ""
)

// buildEpoch parses the embedded BuildEpoch value, using zero when it is unset
// or invalid.
func buildEpoch() int64 {
	n, err := strconv.ParseInt(BuildEpoch, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// init derives the RFC 3339 UTC build date from the embedded epoch.
func init() {
	if epoch := buildEpoch(); epoch > 0 {
		BuildDate = time.Unix(epoch, 0).UTC().Format(time.RFC3339)
	}
}
