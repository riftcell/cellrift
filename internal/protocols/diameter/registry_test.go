package diameter_test

// This file's pinned AVP code-set literals exist because the pre-existing
// TestAdapter_Send* tests only assert AVP presence ("Origin-Host is in the
// response"), never the complete set on the wire — they would stay green
// even if a new AVP were silently added to UAR or CCR. These two tests are
// the actual mechanical proof that the command-registry refactor which
// follows in this package does not change what goes out on the wire for the
// two commands that already worked before it: the literals below were
// captured and committed against the switch-based resolveCommand, before it
// was touched, and must still hold true after the refactor lands.

import (
	"sort"
	"testing"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/protocols/diameter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sentAVPCodes sends nothing itself — it reads back the raw bytes hss last
// received, parses them, and returns every top-level AVP code found, sorted
// ascending. Sorting is required because Go randomises map iteration order
// over a scenario step's `fields`, so the AVP encoding order (and therefore
// an unsorted comparison) would be flaky across runs.
func sentAVPCodes(t *testing.T, hss *diameter.MockHSS) []uint32 {
	t.Helper()

	raw := hss.LastRequestRaw()
	require.NotEmpty(t, raw, "MockHSS recorded no request")

	msg, err := diameter.ParseMessage(raw)
	require.NoError(t, err)

	codes := make([]uint32, 0, len(msg.AVPs))
	for _, avp := range msg.AVPs {
		codes = append(codes, avp.Code)
	}
	sort.Slice(codes, func(i, j int) bool { return codes[i] < codes[j] })
	return codes
}

func TestUAR_AVPCodeSetIsPinned(t *testing.T) {
	hss := diameter.NewMockHSS(t)
	defer hss.Close()

	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)

	adapter := diameter.NewAdapterWithTransport(tr, 2*time.Second)
	defer adapter.Close()

	fields := map[string]string{
		"Session-Id":      "cellrift;test;uar-pin",
		"Origin-Host":     "cellrift.ims.local",
		"Origin-Realm":    "ims.local",
		"Public-Identity": "sip:447700900001@ims.local",
	}

	result, err := adapter.Execute("send", hss.Addr(), "UAR", fields, nil)
	require.NoError(t, err)
	require.Equal(t, common.StatusPassed, result.Status, "UAR should succeed: %s", result.Error)

	// Every AVP TS 29.229 §6.1.1's UAR ABNF marks mandatory, plus the two
	// this step declares. Three of them are auto-injected because no
	// scenario field supplies them, and each was added only after a real
	// Open5GS/freeDiameter peer answered 5005 DIAMETER_MISSING_AVP naming
	// it (test/interop/EVIDENCE.md): 260 Vendor-Specific-Application-Id
	// (run 1), 263 Session-Id (run 2), and 277 Auth-Session-State, which
	// the ABNF requires and which this pin previously locked OUT of the
	// UAR. 283 Destination-Realm is derived from the CEA's Origin-Realm;
	// 264/296 and 601 come from the step's own fields.
	assert.Equal(t, []uint32{260, 263, 264, 277, 283, 296, 601}, sentAVPCodes(t, hss))
}

func TestCCR_AVPCodeSetIsPinned(t *testing.T) {
	hss := diameter.NewMockHSS(t)
	defer hss.Close()

	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)

	adapter := diameter.NewAdapterWithTransport(tr, 2*time.Second)
	defer adapter.Close()

	fields := map[string]string{
		"Session-Id":        "cellrift;test;ccr-pin",
		"Origin-Host":       "cellrift.ims.local",
		"Origin-Realm":      "ims.local",
		"CC-Request-Type":   "1",
		"CC-Request-Number": "0",
	}

	result, err := adapter.Execute("send", hss.Addr(), "CCR", fields, nil)
	require.NoError(t, err)
	require.Equal(t, common.StatusPassed, result.Status, "CCR should succeed: %s", result.Error)

	// 258 Auth-Application-Id (auto-injected: Credit-Control is a BARE
	// application, so it carries this where a vendor-scoped command carries
	// Vendor-Specific-Application-Id instead — RFC 4006 §3.1 marks it
	// mandatory, and a real SigScale OCS answered 5005 without it),
	// 263 Session-Id, 264 Origin-Host, 283 Destination-Realm (auto-injected),
	// 296 Origin-Realm, 415 CC-Request-Number, 416 CC-Request-Type.
	//
	// Note what is NOT here: 260 Vendor-Specific-Application-Id. A command is
	// either bare or vendor-scoped, never both, and this pin is what keeps
	// that true for CCR.
	assert.Equal(t, []uint32{258, 263, 264, 283, 296, 415, 416}, sentAVPCodes(t, hss))
}
