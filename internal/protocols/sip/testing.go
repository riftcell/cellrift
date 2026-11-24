package sip

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/stretchr/testify/require"
)

// AdapterCall records a single call to the adapter's Execute method.
type AdapterCall struct {
	Action  string
	Target  string
	Message string
	Fields  map[string]string
}

// LastCalls returns all calls made to the adapter.
// This is only populated for adapters created via NewAdapterWithTransport.
func (a *Adapter) LastCalls() []AdapterCall {
	// Not tracked in production — only meaningful in tests.
	// We need a separate mechanism for this.
	return nil
}

// TrackingAdapter wraps an Adapter and records all calls for test assertions.
type TrackingAdapter struct {
	*Adapter
	mu    sync.Mutex
	calls []AdapterCall
}

// NewTrackingAdapter creates a SIP adapter that records all Execute calls.
// The wrapped adapter is always built with the "udp" transport type: the
// tracking adapter exists to record Execute calls for test assertions and
// never puts a Via header on the wire itself, so the token is inert here.
func NewTrackingAdapter(tr common.Transport, timeout time.Duration) *TrackingAdapter {
	return &TrackingAdapter{
		Adapter: NewAdapterWithTransport(tr, "udp", timeout),
	}
}

// Execute delegates to the wrapped adapter and records the call.
func (ta *TrackingAdapter) Execute(action, target, message string, fields map[string]string, config map[string]string) (*common.StepResult, error) {
	ta.mu.Lock()
	fieldsCopy := make(map[string]string, len(fields))
	for k, v := range fields {
		fieldsCopy[k] = v
	}
	ta.calls = append(ta.calls, AdapterCall{
		Action:  action,
		Target:  target,
		Message: message,
		Fields:  fieldsCopy,
	})
	ta.mu.Unlock()

	return ta.Adapter.Execute(action, target, message, fields, config)
}

// Calls returns all recorded calls.
func (ta *TrackingAdapter) Calls() []AdapterCall {
	ta.mu.Lock()
	defer ta.mu.Unlock()
	return append([]AdapterCall{}, ta.calls...)
}

// RecordedSIPRequest is one SIP request this mock read off the socket and
// successfully parsed, kept in arrival order. Method/CallID/CSeq are lifted
// from the parsed message so a test can identify a request without
// re-parsing Raw — for a duplicated send, two entries sharing one Call-ID
// and one CSeq is the direct evidence that the same message arrived twice.
//
// Scope: only the UDP MockUASServer keeps a history. TCPMockUASServer and
// TLSMockUASServer each serve exactly one request per accepted connection
// and close it, so neither can observe the repeated or reordered arrivals
// on one connection that this type exists to record.
type RecordedSIPRequest struct {
	Raw        []byte
	Method     string
	CallID     string
	CSeq       string
	ReceivedAt time.Time
}

// MockUASServer is an exported in-process mock SIP UAS for cross-package tests.
type MockUASServer struct {
	conn   *net.UDPConn
	addr   string
	done   chan struct{}
	wg     sync.WaitGroup
	mu     sync.Mutex
	errors []error

	// firstRequestAt records when the first request this mock parsed was
	// read off the socket, letting a timing test measure how long a
	// send-side impairment held the bytes back before they ever reached the
	// wire. Zero until the first successful parse.
	firstRequestAt time.Time

	// receivedRequests is every successfully parsed request, in arrival
	// order. Guarded by the same u.mu as firstRequestAt and errors.
	receivedRequests []RecordedSIPRequest

	// rawReads is every datagram this mock read off the socket, captured
	// BEFORE ParseMessage runs. receivedRequests cannot serve this purpose:
	// serve() records a request only after a SUCCESSFUL parse and otherwise
	// takes the `continue` branch, so a deliberately truncated send — which
	// by construction cannot parse — would be invisible to every existing
	// hook. Proving "the peer observed fewer bytes than a whole message" is
	// exactly what a truncated-frame test must assert.
	rawReads [][]byte

	// responder, when set, replaces mockResponses for every request. It is
	// the sad-path hook: the default mock answers every valid request with a
	// success, which is precisely why a false-PASS on a 5xx survived until a
	// review ran a hostile peer against the real binary. Tests that need the
	// peer to misbehave — an error status, no final response, an extra
	// header — set this rather than standing up a bespoke server.
	responder func(req *SIPMessage) []*SIPMessage
}

// SetResponder replaces the mock's default success responses with resp for
// every subsequent request. Pass nil to restore the defaults.
func (u *MockUASServer) SetResponder(resp func(req *SIPMessage) []*SIPMessage) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.responder = resp
}

// ErrorResponder returns a responder that answers every request with the
// given final status, echoing the request's dialog headers so the response
// correlates like a real peer's would. Reason should be the standard phrase
// for code (e.g. "Server Internal Error").
func ErrorResponder(code int, reason string) func(req *SIPMessage) []*SIPMessage {
	return func(req *SIPMessage) []*SIPMessage {
		return []*SIPMessage{BuildResponse(code, reason, mockDialogHeaders(req))}
	}
}

// DigestChallengeResponder answers the FIRST request with status/challenges and
// every later request by consulting verify: nil means "credentials accepted",
// answer 200; a non-nil error means "rejected", answer the same challenge again.
//
// The verification itself deliberately lives in the CALLER's test, not here.
// The expected digest hex must be computed outside production code, and a
// verifier living in this file would be production code by this package's own
// definition (testing.go ships as part of the package, not as a _test file).
//
// status is 401 or 407; challengeHeader is the header name that carries the
// challenges (WWW-Authenticate or Proxy-Authenticate); challenges are raw
// header VALUES. They are joined with ", " into one header, which is
// byte-for-byte what ParseMessage produces from two physical header lines
// (codec.go), so this exercises the same parse path. It is NOT a proof that
// two physical lines arrive correctly; TestParseChallenges in challenge_test.go
// is that proof, using a real peer's captured bytes.
func DigestChallengeResponder(status int, reason, challengeHeader string,
	challenges []string, verify func(req *SIPMessage) error) func(req *SIPMessage) []*SIPMessage {
	var mu sync.Mutex
	seen := 0
	return func(req *SIPMessage) []*SIPMessage {
		mu.Lock()
		seen++
		first := seen == 1
		mu.Unlock()
		h := mockDialogHeaders(req)
		if first || verify(req) != nil {
			h[challengeHeader] = strings.Join(challenges, ", ")
			return []*SIPMessage{BuildResponse(status, reason, h)}
		}
		return []*SIPMessage{BuildResponse(200, "OK", h)}
	}
}

// NewMockUAS starts a mock UAS on a random local port. Exported for use
// in integration tests outside the sip package.
func NewMockUAS(t *testing.T) *MockUASServer {
	t.Helper()
	uas, err := StartMockUAS()
	if err != nil {
		t.Fatalf("mock UAS: %v", err)
	}
	return uas
}

// StartMockUAS is NewMockUAS without a *testing.T: it is what `cellrift
// selftest` calls to stand the same peer up inside the shipped binary, so a
// new user can watch a scenario pass with no external infrastructure. The
// error return replaces t.Fatalf; nothing else differs.
func StartMockUAS() (*MockUASServer, error) {
	udpAddr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("resolve addr: %w", err)
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}

	uas := &MockUASServer{
		conn: conn,
		addr: conn.LocalAddr().String(),
		done: make(chan struct{}),
	}
	uas.wg.Add(1)
	go uas.serve()
	return uas, nil
}

// Addr returns the address the mock UAS is listening on.
func (u *MockUASServer) Addr() string { return u.addr }

// Close shuts down the mock UAS.
func (u *MockUASServer) Close() {
	close(u.done)
	u.conn.Close()
	u.wg.Wait()
}

func (u *MockUASServer) serve() {
	defer u.wg.Done()
	buf := make([]byte, 65535)

	for {
		n, remoteAddr, err := u.conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-u.done:
				return
			default:
				u.addError(err)
				return
			}
		}

		u.recordRawRead(buf[:n])

		msg, err := ParseMessage(buf[:n])
		if err != nil {
			u.addError(fmt.Errorf("mock UAS parse: %w", err))
			continue
		}
		u.recordFirstRequestAt()
		u.recordRequest(buf[:n], msg)

		responses := u.buildResponses(msg)
		for _, resp := range responses {
			raw := Serialise(resp)
			if _, err := u.conn.WriteToUDP(raw, remoteAddr); err != nil {
				u.addError(fmt.Errorf("mock UAS write: %w", err))
			}
		}
	}
}

func (u *MockUASServer) buildResponses(req *SIPMessage) []*SIPMessage {
	u.mu.Lock()
	responder := u.responder
	u.mu.Unlock()
	if responder != nil {
		return responder(req)
	}
	return mockResponses(req)
}

func (u *MockUASServer) addError(err error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.errors = append(u.errors, err)
}

// recordFirstRequestAt stamps firstRequestAt the first time it is called;
// later calls are no-ops so only the very first request's arrival is kept.
func (u *MockUASServer) recordFirstRequestAt() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.firstRequestAt.IsZero() {
		u.firstRequestAt = time.Now()
	}
}

// FirstRequestAt returns when this mock read its first request off the
// socket, or the zero time if it has not received one yet.
func (u *MockUASServer) FirstRequestAt() time.Time {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.firstRequestAt
}

// recordRequest appends one arrival-ordered history entry. raw is copied
// because serve()'s read buffer is reused on every iteration.
func (u *MockUASServer) recordRequest(raw []byte, msg *SIPMessage) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.receivedRequests = append(u.receivedRequests, RecordedSIPRequest{
		Raw:        append([]byte(nil), raw...),
		Method:     msg.Method,
		CallID:     msg.Headers[HeaderCallID],
		CSeq:       msg.Headers[HeaderCSeq],
		ReceivedAt: time.Now(),
	})
}

// ReceivedRequests returns every request this mock parsed, in arrival
// order. Empty until the first one arrives, which is what makes "this step
// was dropped and never reached the peer" directly assertable. Each entry's
// Raw is independently copied so a caller mutating it can never corrupt the
// mock's own state.
func (u *MockUASServer) ReceivedRequests() []RecordedSIPRequest {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := make([]RecordedSIPRequest, len(u.receivedRequests))
	for i, r := range u.receivedRequests {
		r.Raw = append([]byte(nil), r.Raw...)
		out[i] = r
	}
	return out
}

// recordRawRead appends one pre-parse read to the history. raw is copied
// because serve()'s read buffer is reused on every iteration.
// WaitForRequests waits until the mock has recorded at least n parsed
// requests, then returns them. Use it after any step that does not wait for
// a response — a dropped or duplicated send, a connection fault, an
// action: raw — where asserting immediately races the mock's own read
// goroutine; see internal/protocols/common/mockwait.go.
func (u *MockUASServer) WaitForRequests(t *testing.T, n int) []RecordedSIPRequest {
	t.Helper()
	common.WaitUntil(t, fmt.Sprintf("the peer to record %d request(s)", n), func() bool {
		return len(u.ReceivedRequests()) >= n
	})
	return u.ReceivedRequests()
}

// WaitForRawRead waits until the mock's pre-parse read history satisfies
// cond. The predicate form is what a truncated or malformed send needs: the
// history's shape, not merely a count.
func (u *MockUASServer) WaitForRawRead(t *testing.T, what string, cond func(reads [][]byte) bool) [][]byte {
	t.Helper()
	common.WaitUntil(t, what, func() bool { return cond(u.RawReads()) })
	return u.RawReads()
}

func (u *MockUASServer) recordRawRead(raw []byte) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.rawReads = append(u.rawReads, append([]byte(nil), raw...))
}

// RawReads returns every chunk this mock read off the wire, in arrival
// order, before any parsing. Each entry is independently copied so a caller
// mutating one can never corrupt the mock's own state — the same deep-copy
// discipline ReceivedRequests uses.
func (u *MockUASServer) RawReads() [][]byte {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := make([][]byte, len(u.rawReads))
	for i, r := range u.rawReads {
		out[i] = append([]byte(nil), r...)
	}
	return out
}

// mockResponses builds standard SIP responses for a given request.
// Shared by MockUASServer (UDP) and TLSMockUASServer (TLS/TCP).
// mockDialogHeaders echoes the request headers a response must carry to
// correlate with its request (RFC 3261 §8.2.6.2), adding the server's own
// To-tag. Shared by the default success responses and ErrorResponder so a
// forced error still looks like a real peer's answer to THIS request.
func mockDialogHeaders(req *SIPMessage) map[string]string {
	return map[string]string{
		"From":    req.Headers["From"],
		"To":      req.Headers["To"] + ";tag=srv-mock-001",
		"Call-ID": req.Headers["Call-ID"],
		"CSeq":    req.Headers["CSeq"],
		"Via":     req.Headers["Via"],
	}
}

func mockResponses(req *SIPMessage) []*SIPMessage {
	h := mockDialogHeaders(req)
	switch req.Method {
	case "REGISTER":
		resp := BuildResponse(200, "OK", h)
		resp.Headers["Contact"] = req.Headers["Contact"]
		return []*SIPMessage{resp}
	case "INVITE":
		ringing := BuildResponse(180, "Ringing", h)
		ok := BuildResponse(200, "OK", h)
		ok.Headers["Contact"] = req.Headers["Contact"]
		attachSDPAnswer(ok, req)
		return []*SIPMessage{ringing, ok}
	case "BYE":
		return []*SIPMessage{BuildResponse(200, "OK", h)}
	case "OPTIONS":
		return []*SIPMessage{BuildResponse(200, "OK", h)}
	default:
		return []*SIPMessage{BuildResponse(405, "Method Not Allowed", h)}
	}
}

// attachSDPAnswer gives ok an SDP answer when req carried an SDP offer, so a
// scenario's `response.body.sdp.codec` / `.port` captures — the ones
// basic_call.yaml makes — have something to read against the mock. The
// answer accepts the offer's first codec at a fixed port; no media follows.
func attachSDPAnswer(ok, req *SIPMessage) {
	if !strings.Contains(req.Headers[HeaderContentType], "application/sdp") {
		return
	}
	codec, _ := ParseSDP(req.Body)["codec"].(string)
	if codec == "" {
		codec = "PCMA"
	}
	body := strings.Join([]string{
		"v=0", "o=mockuas 0 0 IN IP4 127.0.0.1", "s=mockuas", "c=IN IP4 127.0.0.1", "t=0 0",
		"m=audio 4000 RTP/AVP 8", "a=rtpmap:8 " + codec + "/8000", "a=sendrecv",
	}, "\r\n") + "\r\n"
	ok.Body = []byte(body)
	ok.Headers[HeaderContentType] = "application/sdp"
	ok.Headers[HeaderContentLength] = strconv.Itoa(len(body))
}

// TLSMockUASServer is an in-process mock SIP UAS that accepts TLS/TCP connections.
type TLSMockUASServer struct {
	listener net.Listener
	addr     string
	done     chan struct{}
	wg       sync.WaitGroup
	mu       sync.Mutex
	errors   []error
}

// NewTLSMockUAS starts a TLS mock UAS on a random local port.
// serverCfg must include a certificate (see SelfSignedTLSConfig).
func NewTLSMockUAS(t *testing.T, serverCfg *tls.Config) *TLSMockUASServer {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", serverCfg)
	if err != nil {
		t.Fatalf("TLS mock UAS: listen: %v", err)
	}
	u := &TLSMockUASServer{
		listener: ln,
		addr:     ln.Addr().String(),
		done:     make(chan struct{}),
	}
	u.wg.Add(1)
	go u.serve()
	return u
}

// Addr returns the address the TLS mock UAS is listening on.
func (u *TLSMockUASServer) Addr() string { return u.addr }

// Close shuts down the TLS mock UAS.
func (u *TLSMockUASServer) Close() {
	close(u.done)
	u.listener.Close()
	u.wg.Wait()
}

func (u *TLSMockUASServer) serve() {
	defer u.wg.Done()
	for {
		conn, err := u.listener.Accept()
		if err != nil {
			select {
			case <-u.done:
				return
			default:
				u.addTLSError(err)
				return
			}
		}
		u.wg.Add(1)
		go u.handleConn(conn)
	}
}

func (u *TLSMockUASServer) handleConn(conn net.Conn) {
	defer u.wg.Done()
	defer conn.Close()
	buf := make([]byte, 65535)
	n, err := conn.Read(buf)
	if err != nil {
		u.addTLSError(fmt.Errorf("TLS mock UAS read: %w", err))
		return
	}
	msg, err := ParseMessage(buf[:n])
	if err != nil {
		u.addTLSError(fmt.Errorf("TLS mock UAS parse: %w", err))
		return
	}
	for _, resp := range mockResponses(msg) {
		if _, err := conn.Write(Serialise(resp)); err != nil {
			u.addTLSError(fmt.Errorf("TLS mock UAS write: %w", err))
		}
	}
}

func (u *TLSMockUASServer) addTLSError(err error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.errors = append(u.errors, err)
}

// TCPFault selects the deliberate transport fault a TCPMockUASServer
// inflicts instead of answering a request. Each value corresponds to a
// normal, spec-legal thing a real peer can do to a TCP connection, so the
// adapter under test walks its ordinary error paths rather than a
// synthetic one.
type TCPFault string

const (
	// TCPFaultNone answers normally with mockResponses.
	TCPFaultNone TCPFault = ""
	// TCPFaultReset aborts with an RST instead of a graceful FIN: setting the
	// linger timeout to zero before Close makes the kernel send RST, which
	// the client sees as ECONNRESET on its next read.
	TCPFaultReset TCPFault = "reset"
	// TCPFaultEOF closes cleanly without writing a byte. A raw conn.Read on
	// the client side reports io.EOF ("EOF"), not io.ErrUnexpectedEOF —
	// that convention belongs to io.ReadFull, which this transport does not use.
	TCPFaultEOF TCPFault = "eof"
	// TCPFaultTruncate writes a partial status line and closes, so the client
	// receives bytes it cannot parse into a SIP message.
	TCPFaultTruncate TCPFault = "truncate"
	// TCPFaultSilence reads the request and answers nothing, holding the
	// connection open until Close so the client's own read deadline fires.
	TCPFaultSilence TCPFault = "silence"
)

// truncatedStatusLine is a status line cut short of its reason phrase.
// parseStartLine requires three space-separated parts, so these ten bytes
// are a deterministic parse failure rather than a probabilistic one.
const truncatedStatusLine = "SIP/2.0 20"

// TCPMockUASServer is an in-process mock SIP UAS over plain TCP, able to
// inflict a declared transport fault instead of answering. It exists
// because the UDP mock has no connection to reset or half-close and the
// TLS mock adds a handshake to a test about raw transport faults.
type TCPMockUASServer struct {
	listener net.Listener
	addr     string
	fault    TCPFault
	done     chan struct{}
	wg       sync.WaitGroup
	mu       sync.Mutex
	errors   []error

	// rawReads is every connection's single read, captured BEFORE any
	// parsing. applyFault's TCPFaultTruncate/TCPFaultSilence/etc. never call
	// ParseMessage at all, and even TCPFaultNone's answerNormally only
	// records on a successful parse — so this is the only hook that can
	// observe a deliberately truncated send. Same rationale as
	// MockUASServer.rawReads above.
	rawReads [][]byte
}

// NewTCPMockUAS starts a plain-TCP mock UAS on a random local port that
// applies fault to every connection it accepts.
func NewTCPMockUAS(t *testing.T, fault TCPFault) *TCPMockUASServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("TCP mock UAS: listen: %v", err)
	}
	u := &TCPMockUASServer{
		listener: ln,
		addr:     ln.Addr().String(),
		fault:    fault,
		done:     make(chan struct{}),
	}
	u.wg.Add(1)
	go u.serve()
	return u
}

// Addr returns the address the TCP mock UAS is listening on.
func (u *TCPMockUASServer) Addr() string { return u.addr }

// Close shuts down the TCP mock UAS. For TCPFaultSilence this is also what
// unblocks the held connection: handleConn is parked on <-u.done.
func (u *TCPMockUASServer) Close() {
	close(u.done)
	u.listener.Close()
	u.wg.Wait()
}

// Errors returns transport-level errors the mock itself encountered outside
// the deliberately-inflicted fault paths (see applyFault).
func (u *TCPMockUASServer) Errors() []error {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]error{}, u.errors...)
}

func (u *TCPMockUASServer) serve() {
	defer u.wg.Done()
	for {
		conn, err := u.listener.Accept()
		if err != nil {
			select {
			case <-u.done:
				return
			default:
				u.addTCPError(err)
				return
			}
		}
		u.wg.Add(1)
		go u.handleConn(conn)
	}
}

// handleConn reads exactly one request, so the fault lands only after the
// client's Send has already succeeded and while it is waiting in Receive.
// Unlike TLSMockUASServer, it does not unconditionally defer conn.Close():
// the silence fault must hold the connection open past this function's
// return, and the reset fault must not be followed by a second Close.
// Each applyFault branch closes (or deliberately doesn't) on its own.
func (u *TCPMockUASServer) handleConn(conn net.Conn) {
	defer u.wg.Done()
	buf := make([]byte, 65535)
	n, err := conn.Read(buf)
	if err != nil {
		conn.Close()
		u.addTCPError(fmt.Errorf("TCP mock UAS read: %w", err))
		return
	}
	u.recordRawRead(buf[:n])
	u.applyFault(conn, buf[:n])
}

// recordRawRead appends one pre-fault read to the history. raw is copied
// because handleConn's read buffer is reused on every iteration.
//
// This is a separate method from MockUASServer.recordRawRead rather than a
// shared embedded helper: the two types already carry their own independent
// mu/errors/addError pairs in this file, and introducing a shared embedded
// recorder here would change the locking topology of two mocks for a
// five-line saving.
func (u *TCPMockUASServer) recordRawRead(raw []byte) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.rawReads = append(u.rawReads, append([]byte(nil), raw...))
}

// WaitForRawRead is TCPMockUASServer's equivalent of MockUASServer's: it
// waits for the pre-parse read history to satisfy cond rather than assuming
// the mock's read goroutine has already run.
func (u *TCPMockUASServer) WaitForRawRead(t *testing.T, what string, cond func(reads [][]byte) bool) [][]byte {
	t.Helper()
	common.WaitUntil(t, what, func() bool { return cond(u.RawReads()) })
	return u.RawReads()
}

// RawReads returns every chunk this mock read off the wire, in arrival
// order, before any parsing or fault handling. Each entry is independently
// copied, the same deep-copy discipline MockUASServer.RawReads uses.
func (u *TCPMockUASServer) RawReads() [][]byte {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := make([][]byte, len(u.rawReads))
	for i, r := range u.rawReads {
		out[i] = append([]byte(nil), r...)
	}
	return out
}

// applyFault dispatches on the mock's declared fault and either inflicts it
// or, for TCPFaultNone, answers normally. Faulted connections may make the
// peer's own subsequent read/write error at teardown; those are expected
// outcomes of the fault itself and are not appended to u.errors.
func (u *TCPMockUASServer) applyFault(conn net.Conn, reqRaw []byte) {
	switch u.fault {
	case TCPFaultReset:
		tcpConn, ok := conn.(*net.TCPConn)
		if !ok {
			// A future TLS/other conn type reaching this path is a real
			// defect (the reset fault only makes sense over bare TCP), so
			// record it rather than silently no-op.
			u.addTCPError(fmt.Errorf("TCP mock UAS: reset fault requires *net.TCPConn, got %T", conn))
			conn.Close()
			return
		}
		_ = tcpConn.SetLinger(0)
		tcpConn.Close()
	case TCPFaultEOF:
		conn.Close()
	case TCPFaultTruncate:
		_, _ = conn.Write([]byte(truncatedStatusLine))
		conn.Close()
	case TCPFaultSilence:
		// Hold the connection open, silent, until the mock is torn down —
		// then release it. Returning without the Close leaked one fd per
		// silence-fault connection for the rest of the process.
		<-u.done
		conn.Close()
	default: // TCPFaultNone
		u.answerNormally(conn, reqRaw)
	}
}

// answerNormally parses the request and writes mockResponses(msg), the same
// response builder TLSMockUASServer.handleConn uses.
func (u *TCPMockUASServer) answerNormally(conn net.Conn, reqRaw []byte) {
	defer conn.Close()
	msg, err := ParseMessage(reqRaw)
	if err != nil {
		u.addTCPError(fmt.Errorf("TCP mock UAS parse: %w", err))
		return
	}
	for _, resp := range mockResponses(msg) {
		if _, err := conn.Write(Serialise(resp)); err != nil {
			u.addTCPError(fmt.Errorf("TCP mock UAS write: %w", err))
		}
	}
}

func (u *TCPMockUASServer) addTCPError(err error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.errors = append(u.errors, err)
}

// SelfSignedTLSConfig generates an in-memory self-signed ECDSA cert for tests.
// Returns a server config (cert loaded) and a client config (InsecureSkipVerify).
func SelfSignedTLSConfig(t *testing.T) (serverCfg, clientCfg *tls.Config) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "cellrift-test"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	tlsCert := tls.Certificate{Certificate: [][]byte{certDER}, PrivateKey: key}
	serverCfg = &tls.Config{Certificates: []tls.Certificate{tlsCert}, MinVersion: tls.VersionTLS12}
	clientCfg = &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12} //nolint:gosec
	return serverCfg, clientCfg
}
