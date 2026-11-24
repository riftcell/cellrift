package diameter_test

// session_id_test.go pins RFC 6733 §8.8's Session-Id on every application
// request. Cellrift omitted it entirely: a real Open5GS/freeDiameter Cx peer
// answered UAR with 5005 DIAMETER_MISSING_AVP and "the number of occurences
// (0) is < the rule min (1) for 'Session-Id'", and then could not build a
// well-formed answer either, because its own UAA had no Session-Id to echo.
// The in-repo mock HSS echoed whatever it found — including nothing — so no
// test noticed.

import (
	"encoding/binary"
	"strings"
	"testing"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/protocols/diameter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sessionIDFixture returns an adapter wired to a fresh mock HSS.
func sessionIDFixture(t *testing.T) (*diameter.Adapter, *diameter.MockHSS) {
	t.Helper()
	hss := diameter.NewMockHSS(t)
	t.Cleanup(hss.Close)

	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)
	a := diameter.NewAdapterWithTransport(tr, 2*time.Second)
	t.Cleanup(func() { _ = a.Close() })
	return a, hss
}

// sentAVPsInOrder returns the last request's AVPs as they appear on the
// wire. Order matters here and nowhere else in this package's tests, which
// is why it cannot reuse sentAVPCodes — that helper sorts.
func sentAVPsInOrder(t *testing.T, hss *diameter.MockHSS) []diameter.AVP {
	t.Helper()
	raw := hss.LastRequestRaw()
	require.NotEmpty(t, raw, "the mock recorded no request")
	msg, err := diameter.ParseMessage(raw)
	require.NoError(t, err)
	return msg.AVPs
}

// minimalFields is the smallest field set each command accepts. Session-Id
// injection does not depend on any of them, which is the point: a step that
// declares nothing still produces a spec-shaped request.
var minimalFields = map[string]map[string]string{
	"UAR": {"Public-Identity": "sip:447700900001@ims.local"},
	"MAR": {"Public-Identity": "sip:447700900001@ims.local"},
	"SAR": {"Public-Identity": "sip:447700900001@ims.local"},
	"AIR": {"User-Name": "447700900001"},
	"ULR": {"User-Name": "447700900001"},
	"CCR": {"CC-Request-Type": "1", "CC-Request-Number": "0"},
}

// TestRequest_CarriesGeneratedSessionIDFirst covers all six sendable
// commands, across all three application shapes — vendor-scoped Cx,
// vendor-scoped S6a and bare Credit-Control — because the ABNF of every one
// of them marks Session-Id mandatory.
func TestRequest_CarriesGeneratedSessionIDFirst(t *testing.T) {
	for _, cmd := range []string{"UAR", "MAR", "SAR", "AIR", "ULR", "CCR"} {
		t.Run(cmd, func(t *testing.T) {
			a, hss := sessionIDFixture(t)

			result, err := a.Execute("send", hss.Addr(), cmd, minimalFields[cmd], nil)
			require.NoError(t, err)
			require.Equal(t, common.StatusPassed, result.Status, result.Error)

			avps := sentAVPsInOrder(t, hss)
			require.NotEmpty(t, avps)
			assert.Equal(t, diameter.AVPCodeSessionID, avps[0].Code,
				"RFC 6733 §8.8: Session-Id MUST appear immediately following the Diameter header")

			// The recommended format is
			// <DiameterIdentity>;<high 32>;<low 32>;<optional>.
			id := string(avps[0].Data)
			parts := strings.Split(id, ";")
			require.Len(t, parts, 4, "Session-Id %q must follow the RFC 6733 §8.8 format", id)
			assert.NotEmpty(t, parts[0], "the identity half must name this client")

			assert.Equal(t, 1, countAVPs(avps, diameter.AVPCodeSessionID),
				"exactly one Session-Id, never a second alongside the generated one")
		})
	}
}

func countAVPs(avps []diameter.AVP, code uint32) int {
	n := 0
	for _, avp := range avps {
		if avp.Code == code {
			n++
		}
	}
	return n
}

// TestRequest_StepSuppliedSessionIDIsPreservedAndHoisted is the
// Credit-Control contract: CCR-I, CCR-U and CCR-T belong to ONE session and
// must carry one identifier across three steps, so a declared Session-Id is
// never replaced — only moved to the front.
func TestRequest_StepSuppliedSessionIDIsPreservedAndHoisted(t *testing.T) {
	a, hss := sessionIDFixture(t)
	const declared = "cellrift;credit;001"

	for i, reqType := range []string{"1", "2", "3"} {
		result, err := a.Execute("send", hss.Addr(), "CCR", map[string]string{
			"Session-Id":        declared,
			"CC-Request-Type":   reqType,
			"CC-Request-Number": reqType,
			"User-Name":         "447700900001",
		}, nil)
		require.NoError(t, err)
		require.Equal(t, common.StatusPassed, result.Status, result.Error)

		avps := sentAVPsInOrder(t, hss)
		require.Equal(t, diameter.AVPCodeSessionID, avps[0].Code, "request %d", i+1)
		assert.Equal(t, declared, string(avps[0].Data),
			"a declared Session-Id must survive verbatim: three CCRs in one session share one id")
		assert.Equal(t, 1, countAVPs(avps, diameter.AVPCodeSessionID))
	}
}

// TestRequest_GeneratedSessionIDsAreUnique pins the other half of §8.8:
// generated ids must be unique, so two steps that declare none are two
// sessions, not one.
func TestRequest_GeneratedSessionIDsAreUnique(t *testing.T) {
	a, hss := sessionIDFixture(t)

	seen := make(map[string]bool)
	for i := 0; i < 3; i++ {
		_, err := a.Execute("send", hss.Addr(), "UAR", minimalFields["UAR"], nil)
		require.NoError(t, err)
		id := string(sentAVPsInOrder(t, hss)[0].Data)
		assert.False(t, seen[id], "Session-Id %q was reused across steps", id)
		seen[id] = true
	}
	assert.Len(t, seen, 3)
}

// TestCER_CarriesNoSessionID guards the boundary: Session-Id belongs to
// application requests only. RFC 6733 §5.3's CER has no session, and adding
// one would be its own interop defect.
func TestCER_CarriesNoSessionID(t *testing.T) {
	a, hss := sessionIDFixture(t)

	_, err := a.Execute("send", hss.Addr(), "UAR", minimalFields["UAR"], nil)
	require.NoError(t, err)

	cers := hss.WaitForRequestsOfCommand(t, diameter.CmdCapabilitiesExchange, 1)
	require.Len(t, cers, 1)
	msg, err := diameter.ParseMessage(cers[0].Raw)
	require.NoError(t, err)
	assert.Nil(t, msg.FindAVP(diameter.AVPCodeSessionID),
		"a CER has no session; Session-Id must never be injected into one")
}

// fullUARAVPs is a UAR carrying every AVP RFC 6733 makes mandatory in any
// application request. Each test below removes exactly one of them.
func fullUARAVPs() []diameter.AVP {
	return []diameter.AVP{
		diameter.NewStringAVP(diameter.AVPCodeSessionID, "cellrift;strict;001"),
		diameter.NewStringAVP(diameter.AVPCodeOriginHost, "cellrift.ims.local"),
		diameter.NewStringAVP(diameter.AVPCodeOriginRealm, "ims.local"),
		diameter.NewStringAVP(diameter.AVPCodeUserName, "447700900001"),
	}
}

func withoutAVP(avps []diameter.AVP, code uint32) []diameter.AVP {
	out := make([]diameter.AVP, 0, len(avps))
	for _, avp := range avps {
		if avp.Code != code {
			out = append(out, avp)
		}
	}
	return out
}

// TestStrictMock_RejectsRequestMissingMandatoryAVP is the in-repo stand-in
// for the real peer. Before it, the only thing that would catch a missing
// mandatory AVP was a CI job booting Open5GS in Docker — which is how three
// separate omissions (260, 263, then 264/296) survived a
// green suite, each costing a full CI round trip to find because the peer
// names only the first one it hits. Requests here are hand-built and written
// straight onto the wire, bypassing buildRequestAVPs, so this tests the
// MOCK's rule rather than the adapter's injection.
func TestStrictMock_RejectsRequestMissingMandatoryAVP(t *testing.T) {
	cases := []struct {
		name string
		code uint32
	}{
		{"Session-Id", diameter.AVPCodeSessionID},
		{"Origin-Host", diameter.AVPCodeOriginHost},
		{"Origin-Realm", diameter.AVPCodeOriginRealm},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hss := diameter.NewStrictMockHSS(t)
			defer hss.Close()

			conn := dialMock(t, hss.Addr())
			defer conn.Close()

			cea := sendAndReadMessage(t, conn, buildTestCER(t))
			require.Equal(t, diameter.ResultSuccess, cea.ResultCode(), "the handshake itself must succeed")

			raw, _, err := diameter.BuildRequest(diameter.CmdUserAuthorization, diameter.AppIDCx,
				withoutAVP(fullUARAVPs(), tc.code))
			require.NoError(t, err)

			answer := sendAndReadMessage(t, conn, raw)
			assert.Equal(t, diameter.ResultMissingAVP, answer.ResultCode(),
				"a strict peer must answer 5005 DIAMETER_MISSING_AVP when %s is absent, as a real freeDiameter HSS does", tc.name)
		})
	}
}

// TestStrictMock_AcceptsCompleteRequest is the control: the rule above must
// reject the genuine omissions and nothing else.
func TestStrictMock_AcceptsCompleteRequest(t *testing.T) {
	hss := diameter.NewStrictMockHSS(t)
	defer hss.Close()

	conn := dialMock(t, hss.Addr())
	defer conn.Close()

	sendAndReadMessage(t, conn, buildTestCER(t))

	raw, _, err := diameter.BuildRequest(diameter.CmdUserAuthorization, diameter.AppIDCx, fullUARAVPs())
	require.NoError(t, err)

	answer := sendAndReadMessage(t, conn, raw)
	// A strict mock answers a UAR the way a real Cx peer does: through
	// Experimental-Result, with no base Result-Code at all.
	assert.Zero(t, answer.ResultCode(), "a Cx UAA carries no base Result-Code")
	assert.Equal(t, diameter.ExperimentalFirstRegistration, experimentalCode(t, answer))
}

// experimentalCode digs the Experimental-Result-Code out of an answer.
func experimentalCode(t *testing.T, msg *diameter.Message) uint32 {
	t.Helper()
	group := msg.FindAVP(diameter.AVPCodeExperimentalResult)
	require.NotNil(t, group, "answer carries no Experimental-Result")
	decoded := *group
	require.NoError(t, diameter.DecodeGroupedAVP(&decoded))
	for _, member := range decoded.GroupedAVPs {
		if member.Code == diameter.AVPCodeExperimentalResultCode {
			return binary.BigEndian.Uint32(member.Data)
		}
	}
	t.Fatal("Experimental-Result carries no Experimental-Result-Code")
	return 0
}

// TestRequest_CarriesOriginIdentity pins RFC 6733 §6.3/§6.4 on the adapter
// side: Origin-Host and Origin-Realm are mandatory in EVERY Diameter
// message, and Cellrift put them only in the CER until a real HSS answered
// 5005 naming Origin-Host.
func TestRequest_CarriesOriginIdentity(t *testing.T) {
	a, hss := sessionIDFixture(t)

	result, err := a.Execute("send", hss.Addr(), "UAR", minimalFields["UAR"], nil)
	require.NoError(t, err)
	require.Equal(t, common.StatusPassed, result.Status, result.Error)

	avps := sentAVPsInOrder(t, hss)
	host := findAVPInList(avps, diameter.AVPCodeOriginHost)
	realm := findAVPInList(avps, diameter.AVPCodeOriginRealm)
	require.NotNil(t, host, "Origin-Host (264) is mandatory in every message")
	require.NotNil(t, realm, "Origin-Realm (296) is mandatory in every message")
	assert.NotEmpty(t, string(host.Data))
	assert.NotEmpty(t, string(realm.Data))

	assert.Equal(t, 1, countAVPs(avps, diameter.AVPCodeOriginHost))
	assert.Equal(t, 1, countAVPs(avps, diameter.AVPCodeOriginRealm))
}

// TestRequest_StepSuppliedOriginIdentityWins proves the injection never
// duplicates or overrides a step's own Origin-Host/Origin-Realm.
func TestRequest_StepSuppliedOriginIdentityWins(t *testing.T) {
	a, hss := sessionIDFixture(t)

	result, err := a.Execute("send", hss.Addr(), "UAR", map[string]string{
		"Public-Identity": "sip:447700900001@ims.local",
		"Origin-Host":     "declared.cellrift.local",
		"Origin-Realm":    "declared.local",
	}, nil)
	require.NoError(t, err)
	require.Equal(t, common.StatusPassed, result.Status, result.Error)

	avps := sentAVPsInOrder(t, hss)
	assert.Equal(t, "declared.cellrift.local", string(findAVPInList(avps, diameter.AVPCodeOriginHost).Data))
	assert.Equal(t, "declared.local", string(findAVPInList(avps, diameter.AVPCodeOriginRealm).Data))
	assert.Equal(t, 1, countAVPs(avps, diameter.AVPCodeOriginHost))
	assert.Equal(t, 1, countAVPs(avps, diameter.AVPCodeOriginRealm))
}

// TestUAR_CarriesAuthSessionState pins TS 29.229 §6.1.1's { Auth-Session-State }
// on the UAR. The registry excluded UAR from that injection because a
// regression test pinned its byte shape — pinning a defect, since the Cx
// ABNF marks the AVP mandatory.
func TestUAR_CarriesAuthSessionState(t *testing.T) {
	a, hss := sessionIDFixture(t)

	_, err := a.Execute("send", hss.Addr(), "UAR", minimalFields["UAR"], nil)
	require.NoError(t, err)

	avps := sentAVPsInOrder(t, hss)
	assert.NotNil(t, findAVPInList(avps, diameter.AVPCodeAuthSessionState),
		"TS 29.229 §6.1.1 marks Auth-Session-State mandatory in the UAR")
}

func findAVPInList(avps []diameter.AVP, code uint32) *diameter.AVP {
	for i := range avps {
		if avps[i].Code == code {
			return &avps[i]
		}
	}
	return nil
}
