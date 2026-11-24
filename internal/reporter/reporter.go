package reporter

import (
	"io"

	"github.com/riftcell/cellrift/internal/engine"
	"github.com/riftcell/cellrift/internal/protocols/common"
)

// Reporter formats and writes scenario execution results.
type Reporter interface {
	// Report writes the formatted results to w.
	Report(w io.Writer, results []*engine.RunResult) error
}

// impairDelayKey is the ResponseFields key both protocol adapters populate
// with the number of milliseconds of a step's duration that Cellrift
// inflicted on itself (declared latency, jitter, and post-receive delay).
// Reported separately from DurationMs so a slow step can be attributed:
// DurationMs alone cannot distinguish a slow peer from a deliberately
// delayed client.
const impairDelayKey = "impair_delay_ms"

// impairDelayMs extracts the self-inflicted delay from a step result.
// ok is false when there was no impairment on this step, when the value is
// zero, or when it is not a number — a report never fails because of a
// malformed observability field. One helper rather than three, because all
// three reporters need the identical lookup and type handling. Indexing a
// nil Response map is legal in Go and yields the zero value, so no nil
// check on r.Response is needed here.
func impairDelayMs(r *common.StepResult) (int64, bool) {
	if r == nil {
		return 0, false
	}
	switch v := r.Response[impairDelayKey].(type) {
	case int64:
		return v, v > 0
	case int:
		return int64(v), v > 0
	default:
		return 0, false
	}
}
