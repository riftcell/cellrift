package sip_test

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/protocols/sip"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MockUAS is an in-process mock SIP User Agent Server for testing.
type MockUAS struct {
	conn   *net.UDPConn
	addr   string
	done   chan struct{}
	wg     sync.WaitGroup
	errors []error
	mu     sync.Mutex
}

// NewMockUAS starts a mock UAS on a random local port.
func NewMockUAS() (*MockUAS, error) {
	addr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, err
	}

	uas := &MockUAS{
		conn: conn,
		addr: conn.LocalAddr().String(),
		done: make(chan struct{}),
	}
	uas.wg.Add(1)
	go uas.serve()
	return uas, nil
}

func (u *MockUAS) Addr() string {
	return u.addr
}

func (u *MockUAS) Close() {
	close(u.done)
	u.conn.Close()
	u.wg.Wait()
}

func (u *MockUAS) Errors() []error {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]error{}, u.errors...)
}

func (u *MockUAS) serve() {
	defer u.wg.Done()
	buf := make([]byte, 65535)

	for {
		n, remoteAddr, err := u.conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-u.done:
				return
			default:
				u.recordError(err)
				return
			}
		}

		msg, err := sip.ParseMessage(buf[:n])
		if err != nil {
			u.recordError(fmt.Errorf("mock uas parse: %w", err))
			continue
		}

		responses := u.buildResponses(msg)
		for _, resp := range responses {
			raw := sip.Serialise(resp)
			if _, err := u.conn.WriteToUDP(raw, remoteAddr); err != nil {
				u.recordError(fmt.Errorf("mock uas write: %w", err))
			}
		}
	}
}

func (u *MockUAS) buildResponses(req *sip.SIPMessage) []*sip.SIPMessage {
	commonHeaders := map[string]string{
		"From":    req.Headers["From"],
		"To":      addTag(req.Headers["To"], "srv-mock-001"),
		"Call-ID": req.Headers["Call-ID"],
		"CSeq":    req.Headers["CSeq"],
		"Via":     req.Headers["Via"],
	}

	switch req.Method {
	case "REGISTER":
		resp := sip.BuildResponse(200, "OK", commonHeaders)
		resp.Headers["Contact"] = req.Headers["Contact"]
		return []*sip.SIPMessage{resp}

	case "INVITE":
		// Send 180 Ringing then 200 OK.
		ringing := sip.BuildResponse(180, "Ringing", commonHeaders)
		ok := sip.BuildResponse(200, "OK", commonHeaders)
		ok.Headers["Contact"] = req.Headers["Contact"]
		// Echo an SDP answer when the INVITE carries an SDP offer.
		if strings.Contains(req.Headers["Content-Type"], "application/sdp") {
			answer := "v=0\r\no=mock 0 0 IN IP4 127.0.0.1\r\ns=mock\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio 49170 RTP/AVP 8\r\na=rtpmap:8 PCMA/8000\r\na=sendrecv\r\n"
			ok.Body = []byte(answer)
			ok.Headers["Content-Type"] = "application/sdp"
			ok.Headers["Content-Length"] = fmt.Sprintf("%d", len(answer))
		}
		return []*sip.SIPMessage{ringing, ok}

	case "BYE":
		return []*sip.SIPMessage{sip.BuildResponse(200, "OK", commonHeaders)}

	case "OPTIONS":
		return []*sip.SIPMessage{sip.BuildResponse(200, "OK", commonHeaders)}

	default:
		return []*sip.SIPMessage{sip.BuildResponse(405, "Method Not Allowed", commonHeaders)}
	}
}

func (u *MockUAS) recordError(err error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.errors = append(u.errors, err)
}

func addTag(header, tag string) string {
	if header == "" {
		return ";tag=" + tag
	}
	return header + ";tag=" + tag
}

// newProdMockAndAdapter starts sip.NewMockUAS (production MockUASServer, the
// type under test in this file's new ReceivedRequests tests) paired with a
// real UDP-transport adapter, so requests genuinely cross a socket rather
// than being fed to recordRequest directly.
func newProdMockAndAdapter(t *testing.T, timeout time.Duration) (*sip.MockUASServer, *sip.Adapter) {
	t.Helper()
	mock := sip.NewMockUAS(t)
	tr, err := common.NewUDPTransport(":0")
	require.NoError(t, err)
	adapter := sip.NewAdapterWithTransport(tr, "udp", timeout)
	return mock, adapter
}

// sendRegister sends one REGISTER through adapter to mock and requires it
// succeeded, so each ReceivedRequests test can drive N real sends in one
// line.
func sendRegister(t *testing.T, adapter *sip.Adapter, mockAddr string) {
	t.Helper()
	fields := map[string]string{"To": "sip:alice@example.test", "From": "sip:cellrift@example.test"}
	_, err := adapter.Execute("send", mockAddr, "REGISTER", fields, nil)
	require.NoError(t, err)
}

// TestMockUASServer_ReceivedRequestsRecordsArrivalOrderAndCount proves two
// REGISTERs sent from one adapter over one UDP socket both appear in
// ReceivedRequests(), in arrival order — the accessor a duplicate/reorder
// test asserts against.
func TestMockUASServer_ReceivedRequestsRecordsArrivalOrderAndCount(t *testing.T) {
	mock, adapter := newProdMockAndAdapter(t, 2*time.Second)
	defer mock.Close()
	defer adapter.Close()

	sendRegister(t, adapter, mock.Addr())
	sendRegister(t, adapter, mock.Addr())

	all := mock.ReceivedRequests()
	require.Len(t, all, 2, "both REGISTERs must be recorded")
	assert.Equal(t, "REGISTER", all[0].Method)
	assert.Equal(t, "REGISTER", all[1].Method)
	assert.NotEmpty(t, all[0].CallID)
	assert.NotEmpty(t, all[0].CSeq)
}

// TestMockUASServer_ReceivedRequestsEmptyBeforeAnySend proves "the mock
// never saw the message" is directly assertable — the drop proof this
// history exists to provide.
func TestMockUASServer_ReceivedRequestsEmptyBeforeAnySend(t *testing.T) {
	mock := sip.NewMockUAS(t)
	defer mock.Close()

	assert.Empty(t, mock.ReceivedRequests())
}

// TestMockUASServer_ReceivedRequestsReturnsACopy proves mutating a returned
// slice or a returned entry's Raw bytes cannot corrupt the mock's own state
// or a later caller's view of it.
func TestMockUASServer_ReceivedRequestsReturnsACopy(t *testing.T) {
	mock, adapter := newProdMockAndAdapter(t, 2*time.Second)
	defer mock.Close()
	defer adapter.Close()

	sendRegister(t, adapter, mock.Addr())

	first := mock.ReceivedRequests()
	require.Len(t, first, 1)
	first[0].Raw[0] = 0xFF
	_ = append(first, sip.RecordedSIPRequest{})

	second := mock.ReceivedRequests()
	require.Len(t, second, 1, "appending to a returned slice must not grow the mock's own history")
	assert.NotEqual(t, byte(0xFF), second[0].Raw[0], "mutating a returned Raw slice must not affect a later call's result")
}

// TestMockUASServer_FirstRequestAtUnaffectedByHistory pins that
// FirstRequestAt's existing single-slot behaviour (stamped once, never
// overwritten) still holds once the history is also being recorded.
func TestMockUASServer_FirstRequestAtUnaffectedByHistory(t *testing.T) {
	mock, adapter := newProdMockAndAdapter(t, 2*time.Second)
	defer mock.Close()
	defer adapter.Close()

	assert.True(t, mock.FirstRequestAt().IsZero())

	sendRegister(t, adapter, mock.Addr())
	first := mock.FirstRequestAt()
	assert.False(t, first.IsZero())

	sendRegister(t, adapter, mock.Addr())
	assert.Equal(t, first, mock.FirstRequestAt(), "FirstRequestAt must not be overwritten by a later request")

	assert.Len(t, mock.ReceivedRequests(), 2, "both requests must still be in the history")
}
