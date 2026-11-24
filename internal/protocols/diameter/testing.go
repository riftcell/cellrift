package diameter

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/ishidawataru/sctp"
	"github.com/riftcell/cellrift/internal/protocols/common"
)

// MockHSS is an in-process mock Diameter server (HSS) for testing.
// It listens on TCP and responds to UAR with UAA and CCR with CCA,
// both containing Result-Code = 2001 (DIAMETER_SUCCESS).
//
// Two modes exist, and the difference matters:
//
//   - Lenient (NewMockHSS) answers application requests immediately, with no
//     capabilities exchange. This is NOT how a real Diameter peer behaves. It
//     exercises the AVP codec and message framing in isolation, and a green
//     test here says nothing about interoperability.
//   - Strict (NewStrictMockHSS) enforces RFC 6733 §5.3: the peer state machine
//     starts closed, and any application message arriving before a successful
//     CER/CEA is discarded and the connection torn down. This is how
//     freeDiameter, Open5GS, and production HSS deployments behave.
//
// Cellrift does not implement CER/CEA, so it fails against the strict mock.
// That failure is asserted in TestAdapter_FailsAgainstStrictPeer rather than
// left as a red test, so the gap is encoded in the suite instead of in a
// README sentence someone has to remember to update.
//
// This type lives in package code (not _test.go) so it can be shared across
// package boundaries, exactly like internal/protocols/sip/testing.go's
// MockUASServer/NewMockUAS — the engine's integration test constructs this
// mock via diameter.NewMockHSS(t) instead of carrying its own duplicate.
type MockHSS struct {
	listener net.Listener
	done     chan struct{}
	wg       sync.WaitGroup
	mu       sync.Mutex
	errors   []error

	// strict enforces the RFC 6733 §5.3 capabilities exchange before any
	// application message is answered.
	strict bool
	// rejected counts connections torn down for skipping the handshake.
	rejected int

	// supportedApps is the Application-Id set this mock's CEA advertises and
	// checks a CER against, via the one shared hasCommonApplication
	// intersection (apps.go) — the mock and the client's unsupported-application gate can
	// never silently disagree about what "common application" means.
	supportedApps []uint32

	// lastConn is the most recently accepted connection, tracked so a test
	// can call SendDWR to inject an unsolicited watchdog at an explicit
	// moment — the only non-flaky way to prove a client's DWR-answering path
	// without an idle-timeout sleep.
	lastConn net.Conn
	// conns is every accepted connection, so Close can shut them all. Each
	// handleConn goroutine sits in conn.Read and returns only when that read
	// errors; closing the listener alone never unblocks it. Close used to
	// rely on the CLIENT having hung up first — true in the existing tests
	// only because `defer hss.Close()` is written before
	// `defer adapter.Close()` and defers run in reverse — and hung forever in
	// wg.Wait() the moment that order was inverted or the client never
	// closed. MockHSS is exported, so downstream users hit the same trap.
	conns     []net.Conn
	closeOnce sync.Once
	// writeMu serialises writes to a connection (an answer against an
	// injected DWR racing for the same socket). It is separate from m.mu on
	// purpose: writeChunked blocks in conn.Write and sleeps between chunks,
	// and holding the general mutex across that stalled every other m.mu
	// user — recordRequest, addError, Errors, SendDWR — for the whole
	// duration. Blocking I/O plus a sleep inside the shared critical section
	// is also exactly the shape the earlier writeResponse/addError
	// self-deadlock grew out of.
	writeMu sync.Mutex
	// dwrBeforeAnswer, when set, makes every subsequent application answer
	// preceded by an unsolicited DWR on the wire — it makes the "a DWR
	// arrives while a request is outstanding" case deterministic rather than
	// timing-dependent.
	dwrBeforeAnswer bool

	// lastRequestRaw holds the exact bytes of the most recent Diameter
	// message this mock read off the wire, captured before parsing. Exposed
	// via LastRequestRaw so a test can assert what the client actually
	// encoded — e.g. that Public-Identity carries the RFC 6733 §4.4 V bit —
	// rather than inspecting an AVP struct built before EncodeAVP ever runs.
	lastRequestRaw []byte
	// lastRequestAt is when lastRequestRaw was captured. Exposed via
	// LastRequestAt so a test can bound how quickly a request actually
	// reached this mock — e.g. proving delay_ms is applied by the client
	// AFTER the request has already arrived, not before Send.
	lastRequestAt time.Time

	// receivedRequests is every message this mock framed off the wire, in
	// arrival order, including the CER and any DWR — nothing is filtered,
	// because filtering is the caller's decision (see RequestsOfCommand).
	// Guarded by the same m.mu as lastRequestRaw: a second, uncoordinated
	// lock here would reintroduce the interleaved-write corruption risk the
	// single-mutex discipline in writeResponse already exists to prevent.
	receivedRequests []RecordedDiameterRequest

	// rawReads is every chunk this mock read off the wire, captured BEFORE
	// extractMessage's framing. receivedRequests cannot serve this purpose:
	// recordRequest runs only from drainPending, and drainPending returns
	// early on errIncompleteMessage without recording anything — so a
	// deliberately truncated write, which by construction never completes a
	// frame, reaches neither receivedRequests nor lastRequestRaw. Proving
	// "the peer observed fewer bytes than a whole message" needs this
	// pre-framing view. Guarded by the same m.mu as every other history
	// field, per this type's single-mutex discipline.
	rawReads [][]byte

	// fault is the deliberate transport fault applied to application
	// requests only (never to CER/CEA) — see HSSFault and SetFault.
	fault HSSFault

	// answerChunks is how many separate conn.Write calls one answer is split
	// across. The default (0 or 1) writes the whole answer at once, which is
	// what every test other than the split-read regression wants. Splitting
	// forces session.nextMessage to assemble one logical message from
	// several Receive calls, which is the case injected receive-side delay
	// must charge exactly once.
	answerChunks int
	// lastAnswerWriteCount records how many conn.Write calls the most recent
	// answer actually used (1 for the default path), so a test can confirm
	// SetAnswerChunks was honoured deterministically rather than inferring
	// it from a wall-clock lower bound.
	lastAnswerWriteCount int
}

// RecordedDiameterRequest is one complete Diameter message this mock framed
// off the wire, kept in arrival order. The single-slot lastRequestRaw
// answers "what did the client most recently encode"; this answers "how
// many messages arrived, and in what order", which is the only way to
// observe a deliberately duplicated send (two byte-identical arrivals from
// one step) or a deliberately reordered group (N arrivals whose order
// differs from the order the scenario declared them in).
type RecordedDiameterRequest struct {
	// Raw is a copy of the exact framed bytes, taken before parsing.
	Raw []byte
	// CommandCode and HopByHopID are lifted from the already-parsed message
	// so a test can filter and correlate without re-parsing Raw.
	CommandCode uint32
	HopByHopID  uint32
	ReceivedAt  time.Time
}

// HSSFault selects a deliberate transport fault the mock inflicts INSTEAD
// of answering an application request. Every value is a normal, spec-legal
// thing a real peer can do to a TCP connection, so the client walks its
// ordinary error paths rather than a synthetic one.
//
// Faults deliberately never apply to the CER/CEA exchange: a test needs the
// RFC 6733 §5.3 handshake to succeed first, so the failure it observes is a
// fault on an established session, not a failed connect.
type HSSFault string

const (
	// HSSFaultNone answers application requests normally.
	HSSFaultNone HSSFault = ""
	// HSSFaultReset aborts with an RST instead of a graceful FIN: disabling
	// the socket's linger period before closing makes the kernel send RST,
	// which the client sees as ECONNRESET on its next read or write.
	HSSFaultReset HSSFault = "reset"
	// HSSFaultEOF closes cleanly without answering. The client's raw
	// conn.Read reports io.EOF ("EOF"), not io.ErrUnexpectedEOF — that
	// convention belongs to io.ReadFull, which this transport does not use.
	HSSFaultEOF HSSFault = "eof"
	// HSSFaultTruncate writes the first truncatedAnswerBytes of the real
	// answer and closes. Fewer bytes than the 20-byte Diameter header means
	// the client's framing loop can never extract a message and reads again,
	// so this surfaces as an EOF rather than a codec error.
	HSSFaultTruncate HSSFault = "truncate"
	// HSSFaultSilence reads the request and answers nothing, holding the
	// connection open so the client's own answer deadline is what ends the wait.
	HSSFaultSilence HSSFault = "silence"
)

// truncatedAnswerBytes is how much of a real answer HSSFaultTruncate writes:
// short of the Diameter header length, so no complete message can ever be framed.
const truncatedAnswerBytes = 8

// defaultSupportedApps mirrors the applications the client itself advertises
// (apps.go's supportedVendorApps plus RFC 4006's bare Credit-Control) so the
// pre-existing UAR-only and CCR-only tests keep passing regardless of which
// command a scenario happens to send first.
//
// This is an independent requirement from supportedVendorApps, not a
// duplicate of it: executeSend's post-handshake application gate
// (a.peer.supports(appID)) checks the applications the PEER advertised in
// its CEA, and this mock derives that set from this slice, not from the CER
// it received. Adding AppIDS6a to supportedVendorApps alone would still
// leave an S6a request rejected client-side — "peer does not support
// application 16777251" — before it ever reached the wire, because the
// mock's own CEA would not yet claim S6a back.
var defaultSupportedApps = []uint32{AppIDCx, AppIDCreditControl, AppIDS6a}

// NewMockHSS starts a lenient mock HSS on a random local TCP port. It answers
// application requests without requiring a capabilities exchange — useful for
// codec-level tests, misleading as evidence of interoperability.
func NewMockHSS(t *testing.T) *MockHSS {
	return newMockHSS(t, false, defaultSupportedApps)
}

// NewStrictMockHSS starts a mock HSS that enforces RFC 6733 §5.3: application
// messages received before a successful CER/CEA are discarded and the
// connection is closed, as an RFC-compliant peer would do.
func NewStrictMockHSS(t *testing.T) *MockHSS {
	return newMockHSS(t, true, defaultSupportedApps)
}

// NewMockHSSWithApps starts a mock HSS whose CEA advertises exactly apps —
// the only way to reach the 5010 DIAMETER_NO_COMMON_APPLICATION path (pass a
// set disjoint from the CER's) or to test a specific application
// intersection. Lenient like NewMockHSS: this constructor is about
// CEA content, not the strict pre-handshake gate.
func NewMockHSSWithApps(t *testing.T, apps ...uint32) *MockHSS {
	return newMockHSS(t, false, apps)
}

// newMockHSSOn builds a MockHSS serving on an already-created listener.
// The listener is a parameter rather than created here because SCTP and TCP
// need different listen calls but identical server behaviour: every read,
// framing, dispatch and fault path below is transport-agnostic, operating
// only on the net.Conn Accept returns. *sctp.SCTPListener satisfies
// net.Listener (Accept/Close/Addr), so no adapter type is needed.
func newMockHSSOn(t *testing.T, ln net.Listener, strict bool, apps []uint32) *MockHSS {
	t.Helper()
	return startMockHSSOn(ln, strict, apps)
}

func startMockHSSOn(ln net.Listener, strict bool, apps []uint32) *MockHSS {
	m := &MockHSS{
		listener:      ln,
		done:          make(chan struct{}),
		strict:        strict,
		supportedApps: apps,
	}

	m.wg.Add(1)
	go m.serve()

	return m
}

func newMockHSS(t *testing.T, strict bool, apps []uint32) *MockHSS {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("mock HSS: listen: %v", err)
	}
	return startMockHSSOn(ln, strict, apps)
}

// StartMockHSS is a *testing.T-free equivalent of NewMockHSS /
// NewStrictMockHSS: a TCP loopback HSS advertising the default application
// set, strict (RFC 6733 §5.3 handshake required) or lenient. It exists so
// `cellrift selftest` can stand the same peer up inside the shipped binary;
// the error return replaces t.Fatalf and nothing else differs.
func StartMockHSS(strict bool) (*MockHSS, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}
	return startMockHSSOn(ln, strict, defaultSupportedApps), nil
}

// NewSCTPMockHSS starts a mock HSS listening on a real SCTP association
// rather than TCP, so a test can exercise an SCTP-only code path — notably
// a genuine RFC 4960 ABORT — against the same framing, handshake and answer
// logic every other MockHSS constructor provides.
//
// It skips the calling test when the kernel has no usable SCTP support,
// the same runtime-detection boundary NewSCTPOrFallbackTransport already
// draws in production: SCTP availability is a property of the machine, not
// of this code, and a hard failure here would make the suite red on any
// platform without the sctp module.
//
// The listen address binds one explicit loopback IP, not the wildcard:
// see internal/protocols/common/sctp.go's dialLocalAddr for the full
// multi-homing rationale — a wildcard-bound SCTP socket's Addr().String()
// is slash-joined across every interface and is not a dialable host:port.
func NewSCTPMockHSS(t *testing.T) *MockHSS {
	t.Helper()

	laddr, err := sctp.ResolveSCTPAddr("sctp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("mock HSS: sctp unavailable (resolve): %v", err)
	}
	ln, err := sctp.ListenSCTP("sctp", laddr)
	if err != nil {
		t.Skipf("mock HSS: sctp unavailable (listen): %v", err)
	}
	return newMockHSSOn(t, ln, true, defaultSupportedApps)
}

func (m *MockHSS) serve() {
	defer m.wg.Done()

	for {
		conn, err := m.listener.Accept()
		if err != nil {
			select {
			case <-m.done:
				return
			default:
				m.addError(err)
				return
			}
		}
		m.setLastConn(conn)
		m.wg.Add(1)
		go m.handleConn(conn)
	}
}

// errIncompleteMessage signals extractMessage needs more bytes before it can
// return a complete Diameter message — not a parse failure.
var errIncompleteMessage = errors.New("diameter: incomplete message")

// extractMessage returns the first complete Diameter message in buf (per its
// own Message Length header) and the number of bytes it consumed. TCP has no
// message boundaries: a single conn.Read can legitimately return more than
// one message concatenated together, or a partial one, so the read loop
// must frame on the wire itself rather than assume one Read equals one
// message.
func extractMessage(buf []byte) (*Message, int, error) {
	if len(buf) < HeaderLength {
		return nil, 0, errIncompleteMessage
	}
	msgLen := int(buf[1])<<16 | int(buf[2])<<8 | int(buf[3])
	if msgLen < HeaderLength {
		return nil, 0, fmt.Errorf("diameter: invalid message length %d", msgLen)
	}
	if len(buf) < msgLen {
		return nil, 0, errIncompleteMessage
	}
	msg, err := ParseMessage(buf[:msgLen])
	if err != nil {
		return nil, 0, err
	}
	return msg, msgLen, nil
}

// handleConn serves one accepted connection until the peer closes it or an
// unparseable message arrives. RFC 6733 §5.3's CER/CEA and every application
// request that follows it share this one connection — since the handshake
// fires automatically on the first Diameter step, there is no separate
// "connect" scenario syntax that could open a second connection — a read
// loop with wire framing (extractMessage), not a single read, is what makes
// CER→CEA→UAR→UAA possible on one socket, and what keeps two messages
// written back to back from silently losing one to TCP coalescing.
func (m *MockHSS) handleConn(conn net.Conn) {
	defer m.wg.Done()
	defer conn.Close()

	handshakeDone := false
	var pending []byte
	buf := make([]byte, 65535)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return // peer closed — expected at teardown, not a mock error
		}
		m.recordRawRead(buf[:n])
		pending = append(pending, buf[:n]...)

		if !m.drainPending(conn, &pending, &handshakeDone) {
			return
		}
	}
}

// drainPending dispatches every complete message currently buffered in
// pending, leaving any trailing partial message for the next Read. It
// reports whether the connection should stay open.
func (m *MockHSS) drainPending(conn net.Conn, pending *[]byte, handshakeDone *bool) bool {
	for {
		msg, consumed, err := extractMessage(*pending)
		if errors.Is(err, errIncompleteMessage) {
			return true // wait for more bytes
		}
		if err != nil {
			m.addError(err)
			return false
		}
		m.recordRequest((*pending)[:consumed], msg)
		*pending = (*pending)[consumed:]
		if !m.dispatchOne(conn, msg, handshakeDone) {
			return false
		}
	}
}

// recordRequest stamps the single-slot lastRequestRaw/lastRequestAt AND
// appends one entry to the arrival-ordered history. Both happen here, under
// one lock, so LastRequestRaw() and the final ReceivedRequests() entry can
// never disagree. raw is copied because the buffer it points into is
// reused/reallocated by handleConn's read loop.
func (m *MockHSS) recordRequest(raw []byte, msg *Message) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := append([]byte(nil), raw...)
	now := time.Now()
	m.lastRequestRaw = cp
	m.lastRequestAt = now
	m.receivedRequests = append(m.receivedRequests, RecordedDiameterRequest{
		Raw:         cp,
		CommandCode: msg.CommandCode,
		HopByHopID:  msg.HopByHopID,
		ReceivedAt:  now,
	})
}

// LastRequestAt returns when this mock most recently finished framing a
// complete Diameter message off the wire — used to bound how quickly a
// request actually reached the mock relative to when the client's Execute
// call started, distinguishing "delayed before Send" from "delayed after
// receiving the answer."
func (m *MockHSS) LastRequestAt() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastRequestAt
}

// LastRequestRaw returns the exact bytes of the most recent Diameter message
// this mock received, for tests asserting on wire-level encoding (AVP flags,
// Vendor-Id) rather than on a pre-encode AVP struct.
func (m *MockHSS) LastRequestRaw() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastRequestRaw
}

// ReceivedRequests returns every Diameter message this mock framed off the
// wire, in arrival order. The CER that opens the session and any DWR are
// included — use RequestsOfCommand to narrow to one command. Each entry's
// Raw is independently copied so a caller mutating it can never corrupt the
// mock's own state or a later call's result.
func (m *MockHSS) ReceivedRequests() []RecordedDiameterRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]RecordedDiameterRequest, len(m.receivedRequests))
	for i, r := range m.receivedRequests {
		r.Raw = append([]byte(nil), r.Raw...)
		out[i] = r
	}
	return out
}

// WaitForRequestsOfCommand waits until the peer has recorded at least n
// requests of command code, then returns them. Use it after any step that
// does not wait for an answer — a connection fault, an abort, a duplicate's
// second copy — where the assertion would otherwise race the mock's own
// read goroutine; see internal/protocols/common/mockwait.go.
func (m *MockHSS) WaitForRequestsOfCommand(t *testing.T, code uint32, n int) []RecordedDiameterRequest {
	t.Helper()
	common.WaitUntil(t, fmt.Sprintf("the peer to record %d request(s) of command %d", n, code), func() bool {
		return len(m.RequestsOfCommand(code)) >= n
	})
	return m.RequestsOfCommand(code)
}

// WaitForRawRead waits until the peer's raw read history satisfies cond,
// which receives the history as RawReads returns it. The predicate form is
// what a truncated-send assertion needs: it waits for the SHAPE of the
// history (a final short read), not merely a count, because a request can
// in principle arrive split across reads.
func (m *MockHSS) WaitForRawRead(t *testing.T, what string, cond func(reads [][]byte) bool) [][]byte {
	t.Helper()
	common.WaitUntil(t, what, func() bool { return cond(m.RawReads()) })
	return m.RawReads()
}

// recordRawRead appends one pre-framing read to the history. raw is copied
// because handleConn's read buffer is reused on every iteration.
func (m *MockHSS) recordRawRead(raw []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rawReads = append(m.rawReads, append([]byte(nil), raw...))
}

// RawReads returns every chunk this mock read off the wire, in arrival
// order across every connection, before any framing. Each entry is
// independently copied, the same deep-copy discipline ReceivedRequests uses.
//
// Use this, not ReceivedRequests, to observe a deliberately truncated send:
// a partial frame never reaches recordRequest at all.
func (m *MockHSS) RawReads() [][]byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([][]byte, len(m.rawReads))
	for i, r := range m.rawReads {
		out[i] = append([]byte(nil), r...)
	}
	return out
}

// RequestsOfCommand returns the recorded requests whose command code is
// code, in arrival order. Every session starts with a CER, so a test
// counting application requests would otherwise have to skip it at every
// call site.
func (m *MockHSS) RequestsOfCommand(code uint32) []RecordedDiameterRequest {
	var out []RecordedDiameterRequest
	for _, r := range m.ReceivedRequests() {
		if r.CommandCode == code {
			out = append(out, r)
		}
	}
	return out
}

// dispatchOne answers one parsed message and reports whether the connection
// should stay open. The strict-mode gate is checked against handshakeDone,
// NOT "is this the first message read" — the loop now serves many messages
// per connection, and only a message arriving before a successful CEA is
// actually pre-handshake.
func (m *MockHSS) dispatchOne(conn net.Conn, msg *Message, handshakeDone *bool) bool {
	if m.strict && !*handshakeDone && msg.CommandCode != CmdCapabilitiesExchange {
		m.addRejected()
		return false // deferred conn.Close() in handleConn tears the connection down
	}

	switch msg.CommandCode {
	case CmdCapabilitiesExchange:
		return m.handleCER(conn, msg, handshakeDone)
	case CmdDeviceWatchdog:
		// An inbound DWA (the client answering an unsolicited DWR) — or a
		// client-initiated DWR, which Cellrift does not send yet — is accepted
		// silently: no answer, connection stays open. Without this case a
		// DWA falls into buildApplicationAnswer's default branch and gets
		// answered with a spurious ResultCommandUnsupported error.
		return true
	default:
		return m.answerApplicationRequest(conn, msg)
	}
}

// answerApplicationRequest answers a post-handshake application request,
// optionally preceding the answer with an unsolicited DWR per
// m.dwrBeforeAnswer — the only way to deterministically prove a client's
// DWR-answering path also works while a real application answer is
// outstanding, without an idle-timeout sleep.
func (m *MockHSS) answerApplicationRequest(conn net.Conn, msg *Message) bool {
	if f := m.currentFault(); f != HSSFaultNone {
		return m.applyFault(conn, msg, f)
	}

	if m.strict {
		if missing, ok := missingMandatoryAVP(msg); ok {
			return m.answerMissingAVP(conn, msg, missing)
		}
	}

	if m.dwrBeforeAnswerEnabled() {
		dwr, err := m.buildDWR()
		if err != nil {
			m.addError(err)
			return false
		}
		if !m.writeResponse(conn, dwr) {
			return false
		}
	}

	resp, err := m.buildApplicationAnswer(msg)
	if err != nil {
		m.addError(err)
		return false
	}
	return m.writeResponse(conn, resp)
}

// SetFault makes every subsequent application request be answered with the
// given deliberate transport fault instead of a real answer. Set it after
// constructing the mock and before the client's first application step; the
// handshake is never faulted.
func (m *MockHSS) SetFault(f HSSFault) { m.mu.Lock(); defer m.mu.Unlock(); m.fault = f }

func (m *MockHSS) currentFault() HSSFault { m.mu.Lock(); defer m.mu.Unlock(); return m.fault }

// applyFault inflicts the declared transport fault on conn instead of
// answering msg, and reports whether the connection should stay open.
// Expected teardown conditions here are not recorded as mock errors: the
// fault IS the behaviour under test, not a malfunction.
func (m *MockHSS) applyFault(conn net.Conn, msg *Message, f HSSFault) bool {
	switch f {
	case HSSFaultReset:
		tcpConn, ok := conn.(*net.TCPConn)
		if !ok {
			m.addError(fmt.Errorf("mock HSS: reset fault needs a *net.TCPConn, got %T", conn))
			return false
		}
		_ = tcpConn.SetLinger(0) // 0 => abort with RST on Close, not a graceful FIN
		_ = tcpConn.Close()
		return false
	case HSSFaultEOF:
		_ = conn.Close()
		return false
	case HSSFaultTruncate:
		resp, err := m.buildApplicationAnswer(msg)
		if err != nil {
			m.addError(err)
			return false
		}
		if len(resp) > truncatedAnswerBytes {
			resp = resp[:truncatedAnswerBytes]
		}
		_, _ = conn.Write(resp)
		_ = conn.Close()
		return false
	case HSSFaultSilence:
		<-m.done // hold the connection open, unanswered, until the mock is torn down
		return false
	default:
		return false
	}
}

// handleCER answers req with a CEA and opens the strict-mode gate only when
// that CEA was a 2001 success — a 5010 CEA (no common application) must not
// let subsequent application traffic through.
func (m *MockHSS) handleCER(conn net.Conn, req *Message, handshakeDone *bool) bool {
	resp, err := m.buildCEA(req)
	if err != nil {
		m.addError(err)
		return false
	}
	if !m.writeResponse(conn, resp) {
		return false
	}
	*handshakeDone = hasCommonApplication(req, m.supportedApps)
	return true
}

// answerBuilder builds MockHSS's answer to one application request.
type answerBuilder func(*MockHSS, *Message) ([]byte, error)

// answerBuilders maps a request's command code to the builder that answers
// it. Table-driven so a new command is one map entry, not a switch edit.
type answerBuilders map[uint32]answerBuilder

var applicationAnswers = answerBuilders{
	CmdUserAuthorization:         (*MockHSS).buildUAA,
	CmdCreditControl:             (*MockHSS).buildCCA,
	CmdAuthenticationInformation: (*MockHSS).buildAIA,
	CmdUpdateLocation:            (*MockHSS).buildULA,
	CmdMultimediaAuth:            (*MockHSS).buildMAA,
	CmdServerAssignment:          (*MockHSS).buildSAA,
}

// buildApplicationAnswer dispatches a post-handshake application request to
// its answer builder via applicationAnswers, falling back to buildError for
// any command code this mock has no builder for.
func (m *MockHSS) buildApplicationAnswer(msg *Message) ([]byte, error) {
	if fn, ok := applicationAnswers[msg.CommandCode]; ok {
		return fn(m, msg)
	}
	return m.buildError(msg)
}

// writeResponse writes resp on conn, recording a mock error (not a fatal
// stop) if the write fails, and reports whether the connection should stay
// open. The write itself is serialised on m.mu so a test-triggered SendDWR
// (called from the test's own goroutine) can never interleave its bytes
// with a write the connection's own handleConn goroutine is making at the
// same time. When SetAnswerChunks has configured more than one chunk, and
// resp is long enough to actually split, the write is deferred to
// writeChunked instead — every other caller keeps the original
// single-write behaviour unchanged.
//
// The write itself is serialised by writeMu, and m.mu is taken only for
// the lastAnswerWriteCount assignment — never across conn.Write, and never
// across addError, which takes m.mu itself. A connection-fault step
// (reset/abrupt_close) can make conn.Write fail exactly here, on the mock's
// own answering goroutine; an earlier version held m.mu through the write
// and then called addError, re-locking the same non-reentrant mutex and
// deadlocking this goroutine forever, which hung every later MockHSS call
// including Close's m.wg.Wait.
func (m *MockHSS) writeResponse(conn net.Conn, resp []byte) bool {
	n := m.currentAnswerChunks()

	m.writeMu.Lock()
	writes, err := 1, error(nil)
	if n > 1 && len(resp) >= n {
		writes, err = writeChunked(conn, resp, n)
	} else {
		_, err = conn.Write(resp)
	}
	m.writeMu.Unlock()

	m.mu.Lock()
	m.lastAnswerWriteCount = writes
	m.mu.Unlock()

	if err != nil {
		m.addError(err)
		return false
	}
	return true
}

// answerChunkDelay is the pause writeChunked sleeps between successive
// slices of a split answer — long enough to force the reader on the other
// end through more than one Receive call, short enough not to slow the
// suite.
const answerChunkDelay = 5 * time.Millisecond

// writeChunked writes resp across n roughly-equal successive conn.Write
// calls with answerChunkDelay between each, forcing a reader on the other
// end to assemble one logical message from several physical reads — the
// split-read case that injected receive-side delay must charge exactly once
// for, not once per read. Returns the number of Write calls actually made.
func writeChunked(conn net.Conn, resp []byte, n int) (int, error) {
	size := (len(resp) + n - 1) / n
	writes := 0
	for i := 0; i < len(resp); i += size {
		end := min(i+size, len(resp))
		if _, err := conn.Write(resp[i:end]); err != nil {
			return writes, err
		}
		writes++
		if end < len(resp) {
			time.Sleep(answerChunkDelay)
		}
	}
	return writes, nil
}

// SetAnswerChunks makes every subsequent answer this mock writes split
// across n separate conn.Write calls instead of one. n <= 1 restores the
// default single-write behaviour every other test in this package relies on.
func (m *MockHSS) SetAnswerChunks(n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.answerChunks = n
}

// LastAnswerWriteCount returns how many conn.Write calls the most recent
// answer actually used — the deterministic way a test confirms
// SetAnswerChunks was honoured, rather than inferring it from timing.
func (m *MockHSS) LastAnswerWriteCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastAnswerWriteCount
}

func (m *MockHSS) currentAnswerChunks() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.answerChunks
}

// buildDWR builds an unsolicited DWR: R-bit set (BuildRequest), P-bit clear
// — Capabilities-Exchange and Device-Watchdog must never be proxiable —
// carrying Origin-Host/Origin-Realm.
func (m *MockHSS) buildDWR() ([]byte, error) {
	avps := []AVP{
		newScopedStringAVP(AVPCodeOriginHost, "hss.ims.local"),
		newScopedStringAVP(AVPCodeOriginRealm, "ims.local"),
	}
	raw, _, err := BuildRequest(CmdDeviceWatchdog, 0, avps)
	return raw, err
}

// SendDWR writes an unsolicited DWR onto the most recently accepted
// connection. A test-triggered write is the only non-flaky way to prove a
// client's DWR-answering path: waiting out an idle period instead
// would be exactly the timing-dependent test CLAUDE.md's testing
// conventions rule out.
func (m *MockHSS) SendDWR(t *testing.T) {
	t.Helper()

	conn := m.currentConn()
	if conn == nil {
		t.Fatal("mock HSS: SendDWR called before any connection was accepted")
	}

	raw, err := m.buildDWR()
	if err != nil {
		t.Fatalf("mock HSS: build DWR: %v", err)
	}
	if !m.writeResponse(conn, raw) {
		t.Fatal("mock HSS: write DWR failed")
	}
}

// SetDWRBeforeAnswer configures whether every subsequent application answer
// is preceded by an unsolicited DWR on the wire. This is what makes the
// "a DWR arrives while a UAR is outstanding" case deterministic instead of
// depending on an idle-timeout race.
func (m *MockHSS) SetDWRBeforeAnswer(enabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dwrBeforeAnswer = enabled
}

func (m *MockHSS) dwrBeforeAnswerEnabled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.dwrBeforeAnswer
}

func (m *MockHSS) setLastConn(conn net.Conn) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastConn = conn
	m.conns = append(m.conns, conn)
}

func (m *MockHSS) currentConn() net.Conn {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastConn
}

// buildCEA answers req with a CEA whose Result-Code is 2001 or 5010,
// selected by hasCommonApplication(req, m.supportedApps) — the single
// intersection check shared with the client's own unsupported-application gate (apps.go).
// Both paths are produced by buildCEAWithResult so they can never diverge
// into two implementations that disagree.
func (m *MockHSS) buildCEA(req *Message) ([]byte, error) {
	code := ResultNoCommonApplication
	if hasCommonApplication(req, m.supportedApps) {
		code = ResultSuccess
	}
	return m.buildCEAWithResult(req, code)
}

// buildCEAWithResult builds the CEA body: the RFC 6733 §5.3.1 mandatory set
// (Origin-Host, Origin-Realm, Host-IP-Address, Vendor-Id, Product-Name) plus
// Result-Code and one application AVP per m.supportedApps.
func (m *MockHSS) buildCEAWithResult(req *Message, resultCode uint32) ([]byte, error) {
	avps := []AVP{
		NewUint32AVP(AVPCodeResultCode, resultCode),
		newScopedStringAVP(AVPCodeOriginHost, "hss.ims.local"),
		newScopedStringAVP(AVPCodeOriginRealm, "ims.local"),
		NewAddressAVP(AVPCodeHostIPAddress, net.ParseIP("127.0.0.1")),
		NewUint32AVP(AVPCodeVendorID, VendorID3GPP),
		newScopedStringAVP(AVPCodeProductName, "cellrift-mock-hss"),
	}
	avps = append(avps, m.applicationAVPs()...)

	raw, err := BuildAnswer(CmdCapabilitiesExchange, 0, avps)
	if err != nil {
		return nil, err
	}
	return copyIdentifiers(raw, req), nil
}

// applicationAVPs renders m.supportedApps into CEA application AVPs, using
// the same bare-vs-vendor-wrapped split apps.go's supportedVendorApps
// encodes for the CER: an app matching a supportedVendorApps entry is
// wrapped in Vendor-Specific-Application-Id; anything else (e.g.
// Credit-Control) is a bare Auth-Application-Id.
func (m *MockHSS) applicationAVPs() []AVP {
	var avps []AVP
	for _, app := range m.supportedApps {
		if vsa, ok := vendorScopedApp(app); ok {
			avps = append(avps, NewGroupedAVP(AVPCodeVendorSpecificApplicationID, []AVP{
				NewUint32AVP(AVPCodeVendorID, vsa.VendorID),
				NewUint32AVP(AVPCodeAuthApplicationID, vsa.AppID),
			}))
			continue
		}
		avps = append(avps, NewUint32AVP(AVPCodeAuthApplicationID, app))
	}
	return avps
}

// vendorScopedApp reports whether app is one of apps.go's supportedVendorApps
// entries, and if so returns it — the mock's mirror of the same table the
// client's own CER construction consults, so mock and client can never
// disagree about which applications are vendor-wrapped vs. bare.
func vendorScopedApp(app uint32) (VendorSpecificApp, bool) {
	for _, vsa := range supportedVendorApps {
		if vsa.AppID == app {
			return vsa, true
		}
	}
	return VendorSpecificApp{}, false
}

// buildUAA's Server-Name AVP is routed through newScopedStringAVP rather than
// NewStringAVP directly — before this fix, this was the identical bug the
// client shipped (3GPP AVPs sent without vendor scoping): the mock shared the client's plain-M encoding
// of a 3GPP Cx AVP, so no test comparing mock and client could ever surface
// it. Every string AVP here goes through the same router so no future 3GPP
// AVP added to a mock answer can reintroduce that bug.
// buildUAA answers a User-Authorization-Request. The success verdict it
// carries differs by mode, and the difference is the whole point of strict
// mode existing.
//
// A REAL Cx peer does not answer a UAR with a base Result-Code at all. An
// Open5GS HSS answers a successful one with
// Experimental-Result { Vendor-Id 10415, Experimental-Result-Code 2001
// DIAMETER_FIRST_REGISTRATION } and no Result-Code — captured on the wire
// in test/interop/EVIDENCE.md, run 4. Because this mock answered with a
// base Result-Code instead, `response.Result-Code == 2001` passed here and
// could never pass against the real peer, and Cellrift's own status gate
// read the base field, found nothing, and called the step passed — a false
// pass that a 5001 DIAMETER_ERROR_USER_UNKNOWN would have produced just as
// easily. Strict mode now models what the peer actually sends.
//
// Both modes answer this way. The lenient/strict split is about the RFC 6733
// §5.3 handshake gate, not about what an application answer says, and two
// modes disagreeing over a UAA's verdict would mean no scenario file could
// be correct against both — the exact divergence this change removes.
//
// One deliberate deviation from the real peer, in both modes: Server-Name is
// always included. TS 29.229 returns Server-Capabilities instead on a first
// registration, leaving the I-CSCF to choose an S-CSCF — but the shipped
// multi-protocol scenarios capture Server-Name from the UAA and route a SIP
// REGISTER to it, and giving them no S-CSCF would make those examples about
// the mock's realism rather than about multi-protocol state. Noted here
// rather than hidden: a scenario asserting Server-Name is asserting
// something the real peer does not send on a first registration.
func (m *MockHSS) buildUAA(req *Message) ([]byte, error) {
	avps := []AVP{
		newScopedStringAVP(AVPCodeSessionID, findStringAVP(req, AVPCodeSessionID)),
		m.uaaVerdict(),
		newScopedStringAVP(AVPCodeOriginHost, "hss.ims.local"),
		newScopedStringAVP(AVPCodeOriginRealm, "ims.local"),
		newScopedStringAVP(AVPCodeServerName, "sip:scscf.ims.local:5060"),
	}
	raw, err := BuildAnswer(CmdUserAuthorization, AppIDCx, avps)
	if err != nil {
		return nil, err
	}
	// Copy Hop-by-Hop and End-to-End IDs from request.
	return copyIdentifiers(raw, req), nil
}

// ExperimentalFirstRegistration is 3GPP's DIAMETER_FIRST_REGISTRATION, the
// Experimental-Result-Code a real Cx HSS returns for a successful UAR whose
// subscriber has no S-CSCF assigned yet. It shares the number 2001 with the
// base protocol's DIAMETER_SUCCESS and is a different code in a different
// namespace; only the 2xxx class is common to both.
const ExperimentalFirstRegistration uint32 = 2001

// uaaVerdict returns the success AVP a UAA carries: an Experimental-Result
// group, exactly as a real Cx HSS sends it.
func (m *MockHSS) uaaVerdict() AVP {
	return NewGroupedAVP(AVPCodeExperimentalResult, []AVP{
		NewUint32AVP(AVPCodeVendorID, VendorID3GPP),
		NewUint32AVP(AVPCodeExperimentalResultCode, ExperimentalFirstRegistration),
	})
}

func (m *MockHSS) buildCCA(req *Message) ([]byte, error) {
	avps := []AVP{
		newScopedStringAVP(AVPCodeSessionID, findStringAVP(req, AVPCodeSessionID)),
		NewUint32AVP(AVPCodeResultCode, ResultSuccess),
		newScopedStringAVP(AVPCodeOriginHost, "ocs.ims.local"),
		newScopedStringAVP(AVPCodeOriginRealm, "ims.local"),
	}

	// Echo back CC-Request-Type and CC-Request-Number if present.
	if a := req.FindAVP(AVPCodeCCRequestType); a != nil {
		avps = append(avps, NewUint32AVP(AVPCodeCCRequestType, a.Uint32Val))
	}
	if a := req.FindAVP(AVPCodeCCRequestNumber); a != nil {
		avps = append(avps, NewUint32AVP(AVPCodeCCRequestNumber, a.Uint32Val))
	}

	raw, err := BuildAnswer(CmdCreditControl, AppIDCreditControl, avps)
	if err != nil {
		return nil, err
	}
	return copyIdentifiers(raw, req), nil
}

// MockProvisionedUser and MockProvisionedIMSI are the identities this mock
// has "provisioned". A request naming neither is answered 5003
// DIAMETER_AUTHORIZATION_REJECTED, which is what gives every command a
// distinct failure path to exercise alongside its success path. Both are
// synthetic: an Ofcom test-range MSISDN and a 3GPP test-PLMN (001/01) IMSI,
// never real subscriber data.
//
// There are two because a real HSS keys the two Diameter applications off
// different identities, and the shipped scenarios follow it: S6a's User-Name
// carries the IMSI (TS 29.272), while Cx's carries the IMPI, which for this
// peer is the MSISDN. Interop run 6 found that out the hard way — the S6a
// scenario sent an MSISDN and a real Open5GS HSS answered
// 5001 DIAMETER_ERROR_USER_UNKNOWN. A mock that accepted only one of them
// would force the scenarios to diverge from what the real peer wants, which
// is the divergence this mock exists to avoid.
const (
	MockProvisionedUser = "447700900001"
	MockProvisionedIMSI = "001010000000001"
)

// mockSubscriberKnown reports whether req names a provisioned subscriber. A
// request carrying no User-Name at all makes no subscriber claim and is
// treated as known (User-Name is optional on SAR).
func mockSubscriberKnown(req *Message) bool {
	name := findStringAVP(req, AVPCodeUserName)
	return name == "" || name == MockProvisionedUser || name == MockProvisionedIMSI
}

// answerPreamble builds the Session-Id echo / Result-Code /
// Auth-Session-State / Origin-Host / Origin-Realm block every S6a answer
// this mock builds starts with. Extracted into one helper because
// buildRejected, buildAIA and buildULA all need the identical five-AVP
// block — CLAUDE.md's threshold for a shared helper rather than a third
// copy-pasted block.
func (m *MockHSS) answerPreamble(req *Message, resultCode uint32) []AVP {
	return []AVP{
		newScopedStringAVP(AVPCodeSessionID, findStringAVP(req, AVPCodeSessionID)),
		NewUint32AVP(AVPCodeResultCode, resultCode),
		NewUint32AVP(AVPCodeAuthSessionState, AuthSessionStateNoStateMaintained),
		newScopedStringAVP(AVPCodeOriginHost, "hss.ims.local"),
		newScopedStringAVP(AVPCodeOriginRealm, "ims.local"),
	}
}

// buildRejected answers req with 5003 DIAMETER_AUTHORIZATION_REJECTED — the
// shared error answer every new command's "unknown subscriber" path uses,
// giving AIR and ULR each a distinct, non-2001 outcome to exercise
// alongside their happy path.
func (m *MockHSS) buildRejected(req *Message, appID uint32) ([]byte, error) {
	raw, err := BuildAnswer(req.CommandCode, appID, m.answerPreamble(req, ResultAuthorizationRejected))
	if err != nil {
		return nil, err
	}
	return copyIdentifiers(raw, req), nil
}

// AVP Code vars — 3GPP S6a AVPs this mock's buildAIA/buildULA construct.
// Resolved via mustCode (avp.go) rather than added to avp.go's own S6a
// block, since this file is where they are exclusively used.
var (
	avpCodeAuthenticationInfo    = mustCode("Authentication-Info")
	avpCodeEUTRANVector          = mustCode("E-UTRAN-Vector")
	avpCodeItemNumber            = mustCode("Item-Number")
	avpCodeRAND                  = mustCode("RAND")
	avpCodeXRES                  = mustCode("XRES")
	avpCodeAUTN                  = mustCode("AUTN")
	avpCodeKASME                 = mustCode("KASME")
	avpCodeULAFlags              = mustCode("ULA-Flags")
	avpCodeSubscriptionData      = mustCode("Subscription-Data")
	avpCodeSubscriberStatus      = mustCode("Subscriber-Status")
	avpCodeAccessRestrictionData = mustCode("Access-Restriction-Data")
)

// buildAIA answers an Authentication-Information-Request per 3GPP TS 29.272
// §7.2.6: Session-Id echo, Result-Code, Auth-Session-State, Origin-Host,
// Origin-Realm, and one E-UTRAN authentication vector inside
// Authentication-Info. Vector material is fixed synthetic test data, never
// real key material.
func (m *MockHSS) buildAIA(req *Message) ([]byte, error) {
	if !mockSubscriberKnown(req) {
		return m.buildRejected(req, AppIDS6a)
	}
	avps := append(m.answerPreamble(req, ResultSuccess), mockAuthenticationInfo())
	raw, err := BuildAnswer(CmdAuthenticationInformation, AppIDS6a, avps)
	if err != nil {
		return nil, err
	}
	return copyIdentifiers(raw, req), nil
}

// mockAuthenticationInfo builds Authentication-Info (1413) wrapping one
// E-UTRAN-Vector (1414) with Item-Number 1 and fixed, obviously-synthetic
// 16/16/16/32-byte RAND/XRES/AUTN/KASME fields — never real key material.
func mockAuthenticationInfo() AVP {
	vector := newScopedGroupedAVP(avpCodeEUTRANVector, []AVP{
		NewUint32AVP(avpCodeItemNumber, 1),
		newScopedStringAVP(avpCodeRAND, mockVectorBytes(0xAA, 16)),
		newScopedStringAVP(avpCodeXRES, mockVectorBytes(0xBB, 16)),
		newScopedStringAVP(avpCodeAUTN, mockVectorBytes(0xCC, 16)),
		newScopedStringAVP(avpCodeKASME, mockVectorBytes(0xDD, 32)),
	})
	return newScopedGroupedAVP(avpCodeAuthenticationInfo, []AVP{vector})
}

// mockVectorBytes returns n bytes of a fixed, obviously-synthetic pattern —
// never real key material — used to fill an authentication vector's fields
// so the wire format is exercised without asserting on any particular
// cryptographic value.
func mockVectorBytes(fill byte, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = fill
	}
	return string(b)
}

// buildULA answers an Update-Location-Request per 3GPP TS 29.272 §7.2.4:
// Session-Id echo, Result-Code, Auth-Session-State, Origin-Host,
// Origin-Realm, ULA-Flags, and Subscription-Data carrying
// Subscriber-Status = SERVICE_GRANTED (0) and Access-Restriction-Data = 0.
func (m *MockHSS) buildULA(req *Message) ([]byte, error) {
	if !mockSubscriberKnown(req) {
		return m.buildRejected(req, AppIDS6a)
	}
	avps := append(m.answerPreamble(req, ResultSuccess),
		NewUint32AVP(avpCodeULAFlags, 1),
		mockSubscriptionData(),
	)
	raw, err := BuildAnswer(CmdUpdateLocation, AppIDS6a, avps)
	if err != nil {
		return nil, err
	}
	return copyIdentifiers(raw, req), nil
}

// mockSubscriptionData builds Subscription-Data (1400) with
// Subscriber-Status = SERVICE_GRANTED (0) and Access-Restriction-Data = 0 —
// the minimal member set a scenario asserting on ULA's presence needs.
func mockSubscriptionData() AVP {
	return newScopedGroupedAVP(avpCodeSubscriptionData, []AVP{
		NewUint32AVP(avpCodeSubscriberStatus, 0),
		NewUint32AVP(avpCodeAccessRestrictionData, 0),
	})
}

// AVP Code vars — 3GPP Cx AVPs this mock's buildMAA/buildSAA construct.
// Resolved via mustCode (avp.go), same rationale as the S6a block above:
// this file is where they are exclusively used.
var (
	avpCodeUserData                = mustCode("User-Data")
	avpCodeSIPNumberAuthItems      = mustCode("SIP-Number-Auth-Items")
	avpCodeSIPItemNumber           = mustCode("SIP-Item-Number")
	avpCodeSIPAuthenticationScheme = mustCode("SIP-Authentication-Scheme")
	avpCodeSIPAuthenticate         = mustCode("SIP-Authenticate")
	avpCodeSIPAuthorization        = mustCode("SIP-Authorization")
)

// buildMAA answers a Multimedia-Auth-Request per 3GPP TS 29.229 §6.1.8:
// Session-Id echo, Result-Code, Auth-Session-State, Origin-Host,
// Origin-Realm, the request's own Public-Identity echoed back,
// SIP-Number-Auth-Items = 1, and one SIP-Auth-Data-Item carrying the auth
// material a scenario asserts on. This is the end-to-end proof that a
// scenario's nested SIP-Auth-Data-Item field reaches the wire as a
// vendor-scoped Grouped AVP — see
// TestAdapter_SendMAR_EncodesGroupedAuthDataItemOnTheWire.
func (m *MockHSS) buildMAA(req *Message) ([]byte, error) {
	if !mockSubscriberKnown(req) {
		return m.buildRejected(req, AppIDCx)
	}
	avps := append(m.answerPreamble(req, ResultSuccess),
		newScopedStringAVP(AVPCodePublicID, findStringAVP(req, AVPCodePublicID)),
		newScopedUint32AVP(avpCodeSIPNumberAuthItems, 1),
		mockSIPAuthDataItem(),
	)
	raw, err := BuildAnswer(CmdMultimediaAuth, AppIDCx, avps)
	if err != nil {
		return nil, err
	}
	return copyIdentifiers(raw, req), nil
}

// mockSIPAuthDataItem builds SIP-Auth-Data-Item (612) wrapping
// SIP-Item-Number (613) = 1, SIP-Authentication-Scheme (608) =
// "Digest-AKAv1-MD5", and fixed, obviously-synthetic SIP-Authenticate (609)
// / SIP-Authorization (610) octet strings — never real key material. Per
// 3GPP TS 29.229 §6.1.8.
func mockSIPAuthDataItem() AVP {
	return newScopedGroupedAVP(AVPCodeSIPAuthDataItem, []AVP{
		newScopedUint32AVP(avpCodeSIPItemNumber, 1),
		newScopedStringAVP(avpCodeSIPAuthenticationScheme, "Digest-AKAv1-MD5"),
		newScopedStringAVP(avpCodeSIPAuthenticate, mockVectorBytes(0xEE, 16)),
		newScopedStringAVP(avpCodeSIPAuthorization, mockVectorBytes(0xFF, 16)),
	})
}

// mockUserData is a short, fixed, obviously-synthetic XML-shaped payload
// standing in for a real 3GPP TS 29.328 IMS subscription document — enough
// for a scenario to assert User-Data is present without this mock modelling
// the real schema.
const mockUserData = "<IMSSubscription><PublicIdentity>mock</PublicIdentity></IMSSubscription>"

// buildSAA answers a Server-Assignment-Request per 3GPP TS 29.229 §6.1.4:
// Session-Id echo, Result-Code, Auth-Session-State, Origin-Host,
// Origin-Realm, User-Name and Server-Name echoed back from the request, and
// a synthetic User-Data payload.
func (m *MockHSS) buildSAA(req *Message) ([]byte, error) {
	if !mockSubscriberKnown(req) {
		return m.buildRejected(req, AppIDCx)
	}
	avps := append(m.answerPreamble(req, ResultSuccess),
		newScopedStringAVP(AVPCodeUserName, findStringAVP(req, AVPCodeUserName)),
		newScopedStringAVP(avpCodeUserData, mockUserData),
		newScopedStringAVP(AVPCodeServerName, findStringAVP(req, AVPCodeServerName)),
	)
	raw, err := BuildAnswer(CmdServerAssignment, AppIDCx, avps)
	if err != nil {
		return nil, err
	}
	return copyIdentifiers(raw, req), nil
}

// mandatoryRequestAVPs are the AVPs RFC 6733 requires in every application
// request, whatever the application: Session-Id (§8.8) and the
// Origin-Host / Origin-Realm pair identifying the sender (§6.3, §6.4). A
// real freeDiameter peer rejects a request missing any of them with 5005,
// naming one at a time — which cost this project one CI round trip per AVP
// to discover, because the mock accepted all three omissions happily.
//
// Deliberately not a per-application ABNF: modelling every command's full
// rule set is a dictionary-sized job, and these three are the ones the base
// protocol makes universal.
type mandatoryAVP struct {
	Code uint32
	Name string
}

var mandatoryRequestAVPs = []mandatoryAVP{
	{AVPCodeSessionID, "Session-Id"},
	{AVPCodeOriginHost, "Origin-Host"},
	{AVPCodeOriginRealm, "Origin-Realm"},
}

// missingMandatoryAVP returns the first mandatory AVP req does not carry.
func missingMandatoryAVP(req *Message) (string, bool) {
	for _, want := range mandatoryRequestAVPs {
		if req.FindAVP(want.Code) == nil {
			return want.Name, true
		}
	}
	return "", false
}

// answerMissingAVP answers 5005 DIAMETER_MISSING_AVP, which is what a real
// freeDiameter peer does to a request missing a mandatory AVP — and what
// this mock originally did NOT do. Cellrift sent no Session-Id and
// no Origin-Host/Origin-Realm on any application request; the mock echoed
// whatever it found, including nothing, so the entire suite stayed green
// while a real Open5GS HSS rejected every UAR with exactly this code, one
// AVP per CI round trip. Strict mode exists to model the peer's rules, not
// only its handshake: a request no real peer would accept must not pass
// here either.
//
// missing names the offending AVP. A real peer returns it in a Failed-AVP
// (279); this answer records it in the mock's error list instead, because
// Failed-AVP is not in the embedded dictionary and adding it belongs to a
// dictionary change rather than to this guard.
//
// Strict mode only, deliberately: the lenient mock's documented job is to
// exercise the codec in isolation, and tests that hand-build a partial
// message depend on it answering anyway.
func (m *MockHSS) answerMissingAVP(conn net.Conn, req *Message, missing string) bool {
	m.addError(fmt.Errorf("strict mock: request %d missing mandatory AVP %s", req.CommandCode, missing))
	avps := []AVP{
		NewUint32AVP(AVPCodeResultCode, ResultMissingAVP),
		newScopedStringAVP(AVPCodeOriginHost, "hss.ims.local"),
		newScopedStringAVP(AVPCodeOriginRealm, "ims.local"),
	}
	raw, err := BuildAnswer(req.CommandCode, req.AppID, avps)
	if err != nil {
		m.addError(err)
		return false
	}
	return m.writeResponse(conn, copyIdentifiers(raw, req))
}

func (m *MockHSS) buildError(req *Message) ([]byte, error) {
	avps := []AVP{
		NewUint32AVP(AVPCodeResultCode, ResultCommandUnsupported),
		newScopedStringAVP(AVPCodeOriginHost, "hss.ims.local"),
		newScopedStringAVP(AVPCodeOriginRealm, "ims.local"),
	}
	raw, err := BuildAnswer(req.CommandCode, req.AppID, avps)
	if err != nil {
		return nil, err
	}
	return copyIdentifiers(raw, req), nil
}

// copyIdentifiers copies the Hop-by-Hop and End-to-End IDs from the request
// into the answer, as required by RFC 6733.
func copyIdentifiers(answer []byte, req *Message) []byte {
	answer[12] = byte(req.HopByHopID >> 24)
	answer[13] = byte(req.HopByHopID >> 16)
	answer[14] = byte(req.HopByHopID >> 8)
	answer[15] = byte(req.HopByHopID)
	answer[16] = byte(req.EndToEndID >> 24)
	answer[17] = byte(req.EndToEndID >> 16)
	answer[18] = byte(req.EndToEndID >> 8)
	answer[19] = byte(req.EndToEndID)
	return answer
}

func findStringAVP(msg *Message, code uint32) string {
	a := msg.FindAVP(code)
	if a == nil {
		return ""
	}
	return a.StringVal
}

// Addr returns the address the mock HSS is listening on.
func (m *MockHSS) Addr() string {
	return m.listener.Addr().String()
}

// Close shuts down the mock HSS.
//
// Close is idempotent and never hangs: it closes every accepted connection
// so each handleConn's blocking Read returns, then waits for the goroutines.
// A second Close is a no-op rather than a panic on the closed done channel.
func (m *MockHSS) Close() {
	m.closeOnce.Do(func() {
		close(m.done)
		m.listener.Close()
		m.mu.Lock()
		conns := append([]net.Conn(nil), m.conns...)
		m.mu.Unlock()
		for _, c := range conns {
			c.Close()
		}
		m.wg.Wait()
	})
}

// Errors returns any errors encountered during serving.
func (m *MockHSS) Errors() []error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// A copy, not the backing slice: addError appends to m.errors from the
	// serving goroutines, so handing the live slice to a test would let a
	// later append reallocate under a slice the test is still ranging over.
	// TCPMockUASServer.Errors already copies; the two mocks now agree.
	return append([]error(nil), m.errors...)
}

func (m *MockHSS) addError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.errors = append(m.errors, err)
}

// Rejected returns the number of connections torn down because an application
// message arrived before the RFC 6733 §5.3 capabilities exchange.
func (m *MockHSS) Rejected() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rejected
}

func (m *MockHSS) addRejected() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rejected++
}
