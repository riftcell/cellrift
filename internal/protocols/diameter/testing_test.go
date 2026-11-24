package diameter_test

import (
	"net"
	"testing"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/protocols/diameter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dialMock opens a raw TCP connection to the mock, for tests that need to
// inspect the wire bytes directly rather than going through Adapter.
//
// Deliberately NOT registered via t.Cleanup: handleConn's read loop
// blocks on conn.Read until the client closes its side, and hss.Close()
// calls wg.Wait() to join that goroutine. t.Cleanup callbacks run only after
// the test function's own `defer`s have all fired, so a hss.Close() deferred
// in the test body would wait forever for a t.Cleanup-deferred conn.Close()
// that hasn't run yet. Callers must `defer conn.Close()` themselves, ordered
// so it runs before `defer hss.Close()` (i.e. deferred after it — defers are
// LIFO).
func dialMock(t *testing.T, addr string) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	require.NoError(t, err)
	return conn
}

// sendAndReadMessage writes raw on conn and reads back one Diameter message.
func sendAndReadMessage(t *testing.T, conn net.Conn, raw []byte) *diameter.Message {
	t.Helper()
	_, err := conn.Write(raw)
	require.NoError(t, err)

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 65535)
	n, err := conn.Read(buf)
	require.NoError(t, err)

	msg, err := diameter.ParseMessage(buf[:n])
	require.NoError(t, err)
	return msg
}

func buildTestCER(t *testing.T) []byte {
	t.Helper()
	avps := []diameter.AVP{
		diameter.NewStringAVP(diameter.AVPCodeOriginHost, "cellrift.ims.local"),
		diameter.NewStringAVP(diameter.AVPCodeOriginRealm, "ims.local"),
		diameter.NewAddressAVP(diameter.AVPCodeHostIPAddress, net.ParseIP("127.0.0.1")),
		diameter.NewUint32AVP(diameter.AVPCodeVendorID, diameter.VendorID3GPP),
		diameter.NewStringAVP(diameter.AVPCodeProductName, "cellrift-test"),
		diameter.NewUint32AVP(diameter.AVPCodeAuthApplicationID, diameter.AppIDCreditControl),
		diameter.NewGroupedAVP(diameter.AVPCodeVendorSpecificApplicationID, []diameter.AVP{
			diameter.NewUint32AVP(diameter.AVPCodeVendorID, diameter.VendorID3GPP),
			diameter.NewUint32AVP(diameter.AVPCodeAuthApplicationID, diameter.AppIDCx),
		}),
	}
	raw, _, err := diameter.BuildRequest(diameter.CmdCapabilitiesExchange, 0, avps)
	require.NoError(t, err)
	return raw
}

func buildTestUAR(t *testing.T) []byte {
	t.Helper()
	avps := []diameter.AVP{
		diameter.NewStringAVP(diameter.AVPCodeSessionID, "cellrift;test;uar"),
		diameter.NewStringAVP(diameter.AVPCodeOriginHost, "cellrift.ims.local"),
		diameter.NewStringAVP(diameter.AVPCodeOriginRealm, "ims.local"),
	}
	raw, _, err := diameter.BuildRequest(diameter.CmdUserAuthorization, diameter.AppIDCx, avps)
	require.NoError(t, err)
	return raw
}

// TestMockHSS_ServesCERThenUARonOneConnection proves handleConn is a read
// loop, not a single read: a CER is answered, and a UAR on the SAME socket
// is then also answered — the whole premise of the automatic handshake.
func TestMockHSS_ServesCERThenUARonOneConnection(t *testing.T) {
	hss := diameter.NewMockHSS(t)
	defer hss.Close()

	conn := dialMock(t, hss.Addr())
	defer conn.Close()

	cerRaw := buildTestCER(t)

	cea := sendAndReadMessage(t, conn, cerRaw)
	assert.Equal(t, diameter.CmdCapabilitiesExchange, cea.CommandCode)
	assert.False(t, cea.IsRequest(), "CEA must have the R-bit clear")
	assert.Equal(t, diameter.ResultSuccess, cea.ResultCode())

	reqMsg, err := diameter.ParseMessage(cerRaw)
	require.NoError(t, err)
	assert.Equal(t, reqMsg.HopByHopID, cea.HopByHopID, "CEA must echo the CER's Hop-by-Hop ID")
	assert.Equal(t, reqMsg.EndToEndID, cea.EndToEndID, "CEA must echo the CER's End-to-End ID")

	// Same connection, a UAR now:
	uaa := sendAndReadMessage(t, conn, buildTestUAR(t))
	assert.Equal(t, diameter.CmdUserAuthorization, uaa.CommandCode)
	assert.Equal(t, diameter.ExperimentalFirstRegistration, experimentalCode(t, uaa))
}

// TestMockHSS_UAAEncodesServerNameVendorScoped is the vendor-scoping fix's
// mock-side proof: before it, MockHSS.buildUAA shared the client's exact bug —
// Server-Name (602, 3GPP Cx) was sent plain-M via the same NewStringAVP
// helper — which is why no prior mock-vs-client comparison could ever
// surface the defect a real Open5GS peer rejected. Asserted against the
// wire bytes the mock actually sent
// (ParseMessage of the UAA), not against an AVP struct.
func TestMockHSS_UAAEncodesServerNameVendorScoped(t *testing.T) {
	hss := diameter.NewMockHSS(t)
	defer hss.Close()

	conn := dialMock(t, hss.Addr())
	defer conn.Close()

	uaa := sendAndReadMessage(t, conn, buildTestUAR(t))
	require.Equal(t, diameter.ExperimentalFirstRegistration, experimentalCode(t, uaa))

	serverName := uaa.FindAVP(diameter.AVPCodeServerName)
	require.NotNil(t, serverName, "UAA must carry Server-Name")
	assert.Equal(t, byte(0xC0), serverName.Flags, "Server-Name (602, 3GPP Cx) must be vendor-scoped: V+M")
	assert.Equal(t, diameter.VendorID3GPP, serverName.VendorID)

	// Negative case: Origin-Host (RFC 6733 base) must stay plain-M — the fix
	// must not over-apply to non-3GPP AVPs.
	originHost := uaa.FindAVP(diameter.AVPCodeOriginHost)
	require.NotNil(t, originHost, "UAA must carry Origin-Host")
	assert.Equal(t, byte(0x40), originHost.Flags, "Origin-Host is base-protocol and must not be vendor-scoped")
	assert.Equal(t, uint32(0), originHost.VendorID)
}

// TestMockHSS_CEAAdvertisesItsSupportedApplications proves the 2001 CEA
// actually carries application AVPs a client can read — without this,
// the client's unsupported-application check has nothing real to verify against.
func TestMockHSS_CEAAdvertisesItsSupportedApplications(t *testing.T) {
	hss := diameter.NewMockHSSWithApps(t, diameter.AppIDCx, diameter.AppIDCreditControl)
	defer hss.Close()

	conn := dialMock(t, hss.Addr())
	defer conn.Close()
	cea := sendAndReadMessage(t, conn, buildTestCER(t))

	require.Equal(t, diameter.ResultSuccess, cea.ResultCode())

	apps := advertisedAppsFromWire(t, cea)
	assert.Contains(t, apps, diameter.AppIDCx)
	assert.Contains(t, apps, diameter.AppIDCreditControl)
}

// TestMockHSS_CEAReports5010WhenNoCommonApplication proves the 5010 path is
// reachable purely by configuring the mock with a disjoint application set.
func TestMockHSS_CEAReports5010WhenNoCommonApplication(t *testing.T) {
	hss := diameter.NewMockHSSWithApps(t, diameter.AppIDS6a) // disjoint from the test CER's Cx+CC
	defer hss.Close()

	conn := dialMock(t, hss.Addr())
	defer conn.Close()
	cea := sendAndReadMessage(t, conn, buildTestCER(t))

	assert.Equal(t, diameter.ResultNoCommonApplication, cea.ResultCode())
}

// TestMockHSS_StrictRejectsPreCERApplicationMessage keeps the strict mock's
// pre-handshake gate meaningful: a UAR arriving before any CER
// still tears the connection down and increments Rejected().
func TestMockHSS_StrictRejectsPreCERApplicationMessage(t *testing.T) {
	hss := diameter.NewStrictMockHSS(t)
	defer hss.Close()

	conn := dialMock(t, hss.Addr())
	defer conn.Close()
	_, err := conn.Write(buildTestUAR(t))
	require.NoError(t, err)

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 65535)
	_, readErr := conn.Read(buf)
	assert.Error(t, readErr, "strict mock must tear the connection down before answering")

	assert.Eventually(t, func() bool {
		return hss.Rejected() == 1
	}, 2*time.Second, 10*time.Millisecond)
}

// TestMockHSS_NoErrorsOnClientDisconnect pins adapter_test.go:147's
// assert.Empty(t, hss.Errors()) — a clean client disconnect after a
// successful handshake must not be recorded as a mock error.
func TestMockHSS_NoErrorsOnClientDisconnect(t *testing.T) {
	hss := diameter.NewMockHSS(t)
	defer hss.Close()

	conn := dialMock(t, hss.Addr())
	sendAndReadMessage(t, conn, buildTestCER(t))
	sendAndReadMessage(t, conn, buildTestUAR(t))
	conn.Close()

	assert.Eventually(t, func() bool {
		return len(hss.Errors()) == 0
	}, 2*time.Second, 10*time.Millisecond)
	assert.Empty(t, hss.Errors())
}

// TestMockHSS_SendDWRWritesAWatchdogRequest proves a test can inject an
// unsolicited DWR at an explicit moment: the bytes read back parse as
// CommandCode 280 with the R-bit SET and the P-bit CLEAR — P must never be
// set on 257/280 messages.
func TestMockHSS_SendDWRWritesAWatchdogRequest(t *testing.T) {
	hss := diameter.NewMockHSS(t)
	defer hss.Close()

	conn := dialMock(t, hss.Addr())
	defer conn.Close()

	sendAndReadMessage(t, conn, buildTestCER(t)) // complete the handshake first

	hss.SendDWR(t)

	msgs := readMessages(t, conn, 1, 2*time.Second)
	dwr := msgs[0]
	assert.Equal(t, diameter.CmdDeviceWatchdog, dwr.CommandCode)
	assert.True(t, dwr.IsRequest(), "DWR must have the R-bit SET")
	assert.Zero(t, dwr.Flags&diameter.FlagProxiable, "DWR must never set the P-bit — a set P-bit on a 257/280 message draws INVALID_HDR_BITS (3008) from real peers")
}

// TestMockHSS_AcceptsDWAWithoutTearingDown proves an inbound DWA (the
// client's answer to an unsolicited DWR) is not mistaken for an unknown
// command: the same connection still serves a UAR afterward.
func TestMockHSS_AcceptsDWAWithoutTearingDown(t *testing.T) {
	hss := diameter.NewMockHSS(t)
	defer hss.Close()

	conn := dialMock(t, hss.Addr())
	defer conn.Close()

	sendAndReadMessage(t, conn, buildTestCER(t))

	dwaAVPs := []diameter.AVP{
		diameter.NewStringAVP(diameter.AVPCodeOriginHost, "cellrift.ims.local"),
		diameter.NewStringAVP(diameter.AVPCodeOriginRealm, "ims.local"),
	}
	dwa, _, err := diameter.BuildMessage(diameter.CmdDeviceWatchdog, 0, 0, dwaAVPs) // R-bit clear: an answer
	require.NoError(t, err)
	_, err = conn.Write(dwa)
	require.NoError(t, err)

	// No response is expected for the DWA itself. The connection must stay
	// open and still serve a UAR right after it.
	uaa := sendAndReadMessage(t, conn, buildTestUAR(t))
	assert.Equal(t, diameter.CmdUserAuthorization, uaa.CommandCode)
	assert.Equal(t, diameter.ExperimentalFirstRegistration, experimentalCode(t, uaa))
}

// TestMockHSS_DWRBeforeAnswerPrecedesTheApplicationAnswer proves the DWR
// path's hardest case deterministically: with SetDWRBeforeAnswer enabled, the
// messages the client reads back for one UAR contain a DWR ordered before
// the UAA — a DWR arriving WHILE the UAR is outstanding, not on an idle
// connection, and without any sleep-based timing.
func TestMockHSS_DWRBeforeAnswerPrecedesTheApplicationAnswer(t *testing.T) {
	hss := diameter.NewMockHSS(t)
	defer hss.Close()
	hss.SetDWRBeforeAnswer(true)

	conn := dialMock(t, hss.Addr())
	defer conn.Close()

	sendAndReadMessage(t, conn, buildTestCER(t))

	_, err := conn.Write(buildTestUAR(t))
	require.NoError(t, err)

	msgs := readMessages(t, conn, 2, 2*time.Second)
	require.Len(t, msgs, 2)
	assert.Equal(t, diameter.CmdDeviceWatchdog, msgs[0].CommandCode, "DWR must precede the application answer")
	assert.Equal(t, diameter.CmdUserAuthorization, msgs[1].CommandCode)
}

// readMessages reads from conn until it has decoded exactly want complete
// Diameter messages, accumulating across as many underlying Reads as
// necessary — TCP has no message boundaries, so two messages written back
// to back may arrive in one Read or several. This is
// what lets a test assert message ORDER deterministically without depending
// on whether the OS happens to coalesce the writes.
func readMessages(t *testing.T, conn net.Conn, want int, timeout time.Duration) []*diameter.Message {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var pending []byte
	buf := make([]byte, 65535)
	for countCompleteMessages(pending) < want {
		require.True(t, time.Now().Before(deadline), "timed out waiting for %d message(s)", want)
		conn.SetReadDeadline(deadline)
		n, err := conn.Read(buf)
		require.NoError(t, err)
		pending = append(pending, buf[:n]...)
	}
	return parseWireMessages(t, pending)
}

// countCompleteMessages reports how many full Diameter messages (per their
// own Message Length header) buf currently holds, ignoring any trailing
// partial message.
func countCompleteMessages(buf []byte) int {
	count := 0
	for len(buf) >= diameter.HeaderLength {
		msgLen := int(buf[1])<<16 | int(buf[2])<<8 | int(buf[3])
		if msgLen < diameter.HeaderLength || len(buf) < msgLen {
			break
		}
		count++
		buf = buf[msgLen:]
	}
	return count
}

// parseWireMessages walks raw as a sequence of concatenated Diameter
// messages. diameter.ParseMessage only ever consumes the first one (per its
// own Message Length header), silently ignoring any trailing bytes — this
// helper repeats it until raw is exhausted.
func parseWireMessages(t *testing.T, raw []byte) []*diameter.Message {
	t.Helper()
	var msgs []*diameter.Message
	for len(raw) > 0 {
		require.GreaterOrEqual(t, len(raw), diameter.HeaderLength)
		msgLen := int(raw[1])<<16 | int(raw[2])<<8 | int(raw[3])
		require.LessOrEqual(t, msgLen, len(raw))
		msg, err := diameter.ParseMessage(raw[:msgLen])
		require.NoError(t, err)
		msgs = append(msgs, msg)
		raw = raw[msgLen:]
	}
	return msgs
}

// TestMockHSS_ReceivedRequestsRecordsArrivalOrderAndCount proves the request
// history: a CER followed by two UARs on one connection all appear in
// ReceivedRequests(), in arrival order, and RequestsOfCommand narrows to
// exactly the two application requests — the accessor a UAR-counting test
// uses, since the CER is deliberately not filtered out.
func TestMockHSS_ReceivedRequestsRecordsArrivalOrderAndCount(t *testing.T) {
	hss := diameter.NewMockHSS(t)
	defer hss.Close()

	conn := dialMock(t, hss.Addr())
	defer conn.Close()

	cerRaw := buildTestCER(t)
	sendAndReadMessage(t, conn, cerRaw)
	sendAndReadMessage(t, conn, buildTestUAR(t))
	sendAndReadMessage(t, conn, buildTestUAR(t))

	all := hss.ReceivedRequests()
	require.Len(t, all, 3, "CER + two UARs must all be recorded")
	assert.Equal(t, diameter.CmdCapabilitiesExchange, all[0].CommandCode, "CER must not be filtered out")
	assert.Equal(t, diameter.CmdUserAuthorization, all[1].CommandCode)
	assert.Equal(t, diameter.CmdUserAuthorization, all[2].CommandCode)
	assert.NotEqual(t, all[1].HopByHopID, all[2].HopByHopID, "each request has a distinct Hop-by-Hop ID")

	uars := hss.RequestsOfCommand(diameter.CmdUserAuthorization)
	require.Len(t, uars, 2, "RequestsOfCommand must exclude the CER")

	assert.Equal(t, hss.LastRequestRaw(), all[len(all)-1].Raw, "LastRequestRaw must agree with the final history entry")
}

// TestMockHSS_ReceivedRequestsReturnsACopy proves mutating a returned slice
// or a returned entry's Raw bytes cannot corrupt the mock's own state or a
// later caller's view of it.
func TestMockHSS_ReceivedRequestsReturnsACopy(t *testing.T) {
	hss := diameter.NewMockHSS(t)
	defer hss.Close()

	conn := dialMock(t, hss.Addr())
	defer conn.Close()

	sendAndReadMessage(t, conn, buildTestCER(t))

	first := hss.ReceivedRequests()
	require.Len(t, first, 1)
	first[0].Raw[0] = 0xFF
	_ = append(first, diameter.RecordedDiameterRequest{})

	second := hss.ReceivedRequests()
	require.Len(t, second, 1, "appending to a returned slice must not grow the mock's own history")
	assert.NotEqual(t, byte(0xFF), second[0].Raw[0], "mutating a returned Raw slice must not affect a later call's result")
}

// TestMockHSS_SplitMessageRecordedOnce pins that a message split across two
// conn.Write calls (and therefore two conn.Read calls on the mock's side) is
// recorded exactly once as one complete message, not once per partial read.
func TestMockHSS_SplitMessageRecordedOnce(t *testing.T) {
	hss := diameter.NewMockHSS(t)
	defer hss.Close()

	conn := dialMock(t, hss.Addr())
	defer conn.Close()

	cerRaw := buildTestCER(t)
	split := len(cerRaw) / 2
	_, err := conn.Write(cerRaw[:split])
	require.NoError(t, err)
	time.Sleep(20 * time.Millisecond) // give the mock a chance to read the partial write separately
	_, err = conn.Write(cerRaw[split:])
	require.NoError(t, err)

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 65535)
	_, err = conn.Read(buf)
	require.NoError(t, err)

	all := hss.ReceivedRequests()
	require.Len(t, all, 1, "a message split across two reads must be recorded exactly once")
}

// TestMockHSS_RawReadsCapturesPartialFrame pins a fact that is easy to get
// wrong by reading the mock's API rather than its body: drainPending's
// errIncompleteMessage early return means a partial frame never reaches
// recordRequest, so RequestsOfCommand cannot observe it — only RawReads()'s
// pre-framing view can. len(RawReads()) is asserted > 0 rather than == 1
// because the CER itself is already one read; the truncated write is
// RawReads()'s LAST entry.
func TestMockHSS_RawReadsCapturesPartialFrame(t *testing.T) {
	hss := diameter.NewMockHSS(t)
	defer hss.Close()

	conn := dialMock(t, hss.Addr())
	defer conn.Close()

	sendAndReadMessage(t, conn, buildTestCER(t))

	partial := []byte("REGISTER") // 8 bytes: far short of HeaderLength (20)
	_, err := conn.Write(partial)
	require.NoError(t, err)

	hss.WaitForRawRead(t, "the mock to record the partial frame it can never complete", func(reads [][]byte) bool {
		return len(reads) > 0 && len(reads[len(reads)-1]) == len(partial)
	})

	reads := hss.RawReads()
	require.Greater(t, len(reads), 0, "the CER's own read must already be present")
	assert.Equal(t, partial, reads[len(reads)-1], "the truncated write must be RawReads()'s last entry")

	assert.Empty(t, hss.RequestsOfCommand(diameter.CmdUserAuthorization), "a partial frame must never be framed into a recorded request")
}

// TestMockHSS_RawReadsCopySemantics proves mutating a slice returned by
// RawReads(), or a returned entry, can never change what a later RawReads()
// call returns.
func TestMockHSS_RawReadsCopySemantics(t *testing.T) {
	hss := diameter.NewMockHSS(t)
	defer hss.Close()

	conn := dialMock(t, hss.Addr())
	defer conn.Close()

	sendAndReadMessage(t, conn, buildTestCER(t))

	hss.WaitForRawRead(t, "the mock to record the arrival", func(reads [][]byte) bool { return len(reads) > 0 })

	first := hss.RawReads()
	require.NotEmpty(t, first)
	first[0][0] = 0xFF
	first = append(first, []byte("extra"))

	second := hss.RawReads()
	require.Len(t, second, len(first)-1, "appending to a returned slice must not grow the mock's own history")
	assert.NotEqual(t, byte(0xFF), second[0][0], "mutating a returned entry must not affect a later call's result")
}

// TestSCTPMockHSS_RealAssociationHandshake proves a MockHSS can serve a real
// CER/CEA over a real SCTP association through the production adapter, or
// skip cleanly on a kernel without SCTP — the prerequisite the sctp_abort
// tests need to prove a real transport fault, not a fake one.
func TestSCTPMockHSS_RealAssociationHandshake(t *testing.T) {
	hss := diameter.NewSCTPMockHSS(t) // skips the test if the kernel has no SCTP
	defer hss.Close()

	_, _, err := net.SplitHostPort(hss.Addr())
	require.NoError(t, err, "the single-IP bind must produce a dialable host:port, not a slash-joined multi-homed address")

	tr, warnings, err := common.NewSCTPOrFallbackTransport(":0")
	require.NoError(t, err)
	if len(warnings) > 0 {
		tr.Close()
		t.Skip("kernel substituted TCP for SCTP — a TCP transport cannot dial a real SCTP listener, and asserting against a silently-substituted transport would be a false positive")
	}

	adapter := diameter.NewAdapterWithTransport(tr, 3*time.Second)
	defer adapter.Close()

	fields := map[string]string{
		"Session-Id":      "cellrift;test;sctp-mock-hss",
		"Origin-Host":     "cellrift.ims.local",
		"Origin-Realm":    "ims.local",
		"Public-Identity": "sip:447700900001@ims.local",
	}
	result, err := adapter.Execute("send", hss.Addr(), "UAR", fields, nil)
	require.NoError(t, err)
	assert.Equal(t, common.StatusPassed, result.Status, "UAR over a real SCTP association should succeed: %s", result.Error)

	assert.Len(t, hss.RequestsOfCommand(diameter.CmdCapabilitiesExchange), 1, "the handshake's CER must have reached the mock over the real association")
}

// advertisedAppsFromWire decodes the Application-Id AVPs a wire-parsed
// message carries — bare Auth-Application-Id and vendor-scoped
// Vendor-Specific-Application-Id alike — by round-tripping the CEA through
// the same BuildRequest/ParseMessage path a real client would use. Package
// diameter's own advertisedApplications is unexported, so this test
// (package diameter_test) mirrors its two AVP shapes directly rather than
// duplicating protocol logic in production code just to export it.
func advertisedAppsFromWire(t *testing.T, msg *diameter.Message) []uint32 {
	t.Helper()
	var apps []uint32
	for _, a := range msg.AVPs {
		switch a.Code {
		case diameter.AVPCodeAuthApplicationID:
			apps = append(apps, a.Uint32Val)
		case diameter.AVPCodeVendorSpecificApplicationID:
			sub := a
			require.NoError(t, diameter.DecodeGroupedAVP(&sub))
			if inner := diameter.FindAVP(sub.GroupedAVPs, diameter.AVPCodeAuthApplicationID); inner != nil {
				apps = append(apps, inner.Uint32Val)
			}
		}
	}
	return apps
}

// TestMockHSS_CloseReturnsWhileClientStillConnected pins that Close never
// hangs. handleConn blocks in conn.Read and returns only when that read
// errors; Close used to close only the listener, so wg.Wait() blocked until
// the CLIENT hung up. Every existing test survived on defer ordering alone
// (`defer hss.Close()` written before `defer adapter.Close()`, so the adapter
// closed first). Here the client deliberately never closes: Close must still
// return, because it now closes every accepted connection itself.
func TestMockHSS_CloseReturnsWhileClientStillConnected(t *testing.T) {
	hss := diameter.NewMockHSS(t)

	conn, err := net.Dial("tcp", hss.Addr())
	require.NoError(t, err)
	defer conn.Close() // after the test — the point is that Close does not need this first

	// Give serve() a moment to Accept and spawn handleConn, so the wg is
	// genuinely armed with a goroutine parked in Read.
	require.Eventually(t, func() bool { return len(hss.RawReads()) == 0 && conn != nil }, time.Second, 10*time.Millisecond)
	time.Sleep(50 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		hss.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("MockHSS.Close hung with a client still connected — accepted connections were not closed before wg.Wait()")
	}
}

// TestMockHSS_CloseIsIdempotent pins that a second Close is a no-op rather
// than a panic on the already-closed done channel.
func TestMockHSS_CloseIsIdempotent(t *testing.T) {
	hss := diameter.NewMockHSS(t)
	hss.Close()
	assert.NotPanics(t, hss.Close)
}
