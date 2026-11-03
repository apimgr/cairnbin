package geoip_test

import (
	"os"
	"testing"
	"time"

	"github.com/apimgr/pastebin/src/common/retry"
)

// TestMain shortens the PART 9 backoff so tests that hit an unreachable
// download source do not wait out the production schedule.
func TestMain(m *testing.M) {
	retry.Delays = []time.Duration{0, time.Millisecond}
	os.Exit(m.Run())
}
