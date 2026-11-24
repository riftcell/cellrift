package impair

import (
	"errors"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
)

// fakeTransport is a hand-written common.Transport stand-in: it records
// when each call arrived so tests can assert on injected sleep, and returns
// canned bytes. Hand-written rather than generated because this repository
// uses no mocking framework for transports.
type fakeTransport struct {
	mu          sync.Mutex
	sendTimes   []time.Time
	recvTimes   []time.Time
	recvDelay   time.Duration // how long the fake itself blocks inside Receive
	payload     []byte
	recvErr     error
	closed      bool
	lastTimeout time.Duration
}

func (f *fakeTransport) Send(msg []byte, addr string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sendTimes = append(f.sendTimes, time.Now())
	return nil
}

func (f *fakeTransport) Receive(timeout time.Duration) ([]byte, net.Addr, error) {
	f.mu.Lock()
	f.recvTimes = append(f.recvTimes, time.Now())
	f.lastTimeout = timeout
	delay := f.recvDelay
	err := f.recvErr
	payload := f.payload
	f.mu.Unlock()

	if delay > 0 {
		time.Sleep(delay)
	}
	if err != nil {
		return nil, nil, err
	}
	return payload, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5060}, nil
}

func (f *fakeTransport) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *fakeTransport) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5061}
}

// fakeConnectorTransport embeds *fakeTransport and adds Connect, standing in
// for TCP/TLS/SCTP-shaped transports that hold a connection.
type fakeConnectorTransport struct {
	*fakeTransport
	mu           sync.Mutex
	connectAddrs []string
}

func (f *fakeConnectorTransport) Connect(addr string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connectAddrs = append(f.connectAddrs, addr)
	return nil
}

func TestProfileFromConfig(t *testing.T) {
	cases := []struct {
		name    string
		cfg     map[string]string
		want    Profile
		wantErr bool
	}{
		{
			name: "latency and jitter",
			cfg:  map[string]string{"latency_ms": "200", "jitter_ms": "50"},
			want: Profile{LatencyMs: 200, JitterMs: 50},
		},
		{name: "nil cfg", cfg: nil, want: Profile{}},
		{name: "empty cfg", cfg: map[string]string{}, want: Profile{}},
		{
			name: "unrelated keys ignored",
			cfg:  map[string]string{"timeout_ms": "5000", "origin_host": "x", "check": "y"},
			want: Profile{},
		},
		{
			name:    "non-numeric latency",
			cfg:     map[string]string{"latency_ms": "abc"},
			wantErr: true,
		},
		{
			name:    "negative latency",
			cfg:     map[string]string{"latency_ms": "-5"},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ProfileFromConfig(tc.cfg)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (result %+v)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestWrap_PreservesConnector(t *testing.T) {
	inner := &fakeConnectorTransport{fakeTransport: &fakeTransport{}}
	w := Wrap(inner)

	c, ok := w.(common.Connector)
	if !ok {
		t.Fatalf("Wrap(connector-capable transport) did not satisfy common.Connector")
	}
	if err := c.Connect("host:1"); err != nil {
		t.Fatalf("Connect returned error: %v", err)
	}
	if len(inner.connectAddrs) != 1 || inner.connectAddrs[0] != "host:1" {
		t.Fatalf("Connect did not reach the inner transport: %+v", inner.connectAddrs)
	}
}

// TestWrap_DoesNotPromoteNonConnector guards a concrete regression: a
// wrapped UDP transport must not pass diameter.Adapter.Connect's
// `common.Connector` type-assertion gate.
func TestWrap_DoesNotPromoteNonConnector(t *testing.T) {
	w := Wrap(&fakeTransport{})

	if _, ok := w.(common.Connector); ok {
		t.Fatalf("Wrap(non-connector transport) falsely satisfies common.Connector")
	}
}

func TestWrap_DelegatesCloseAndLocalAddr(t *testing.T) {
	inner := &fakeTransport{}
	w := Wrap(inner)

	if got := w.LocalAddr(); got == nil {
		t.Fatalf("LocalAddr returned nil")
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	if !inner.closed {
		t.Fatalf("Close did not delegate to the inner transport")
	}
}

func TestTransport_SendAppliesLatencyOnce(t *testing.T) {
	inner := &fakeTransport{}
	w := Wrap(inner)
	tr := w.(*Transport)

	if err := tr.SetImpairment(map[string]string{"latency_ms": "200"}); err != nil {
		t.Fatalf("SetImpairment: %v", err)
	}

	start := time.Now()
	if err := w.Send([]byte("a"), "h:1"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 190*time.Millisecond {
		t.Fatalf("first Send took %s, want >= 190ms", elapsed)
	}
	if len(inner.sendTimes) != 1 {
		t.Fatalf("expected exactly one delivery to inner, got %d", len(inner.sendTimes))
	}

	start = time.Now()
	if err := w.Send([]byte("b"), "h:1"); err != nil {
		t.Fatalf("second Send: %v", err)
	}
	elapsed = time.Since(start)
	if elapsed >= 20*time.Millisecond {
		t.Fatalf("second Send took %s, want < 20ms (one-shot arming)", elapsed)
	}
}

func TestTransport_ReceiveDelaysFirstCallOnly(t *testing.T) {
	inner := &fakeTransport{}
	w := Wrap(inner)
	tr := w.(*Transport)

	if err := tr.SetImpairment(map[string]string{"latency_ms": "100"}); err != nil {
		t.Fatalf("SetImpairment: %v", err)
	}

	start := time.Now()
	if _, _, err := w.Receive(5 * time.Second); err != nil {
		t.Fatalf("first Receive: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 90*time.Millisecond {
		t.Fatalf("first Receive took %s, want ~100ms", elapsed)
	}

	for i := 0; i < 2; i++ {
		start = time.Now()
		if _, _, err := w.Receive(5 * time.Second); err != nil {
			t.Fatalf("Receive #%d: %v", i+2, err)
		}
		if elapsed := time.Since(start); elapsed >= 20*time.Millisecond {
			t.Fatalf("Receive #%d took %s, want < 20ms (already consumed)", i+2, elapsed)
		}
	}
}

func TestTransport_JitterWithinBounds(t *testing.T) {
	inner := &fakeTransport{}
	w := Wrap(inner)
	tr := w.(*Transport)

	seen := map[time.Duration]bool{}
	for i := 0; i < 20; i++ {
		if err := tr.SetImpairment(map[string]string{"latency_ms": "50", "jitter_ms": "100"}); err != nil {
			t.Fatalf("SetImpairment iteration %d: %v", i, err)
		}
		start := time.Now()
		if err := w.Send([]byte("x"), "h:1"); err != nil {
			t.Fatalf("Send iteration %d: %v", i, err)
		}
		elapsed := time.Since(start)
		if elapsed < 50*time.Millisecond || elapsed > 150*time.Millisecond+60*time.Millisecond {
			t.Fatalf("iteration %d: elapsed %s outside [50ms, 210ms]", i, elapsed)
		}
		seen[elapsed/(10*time.Millisecond)] = true
	}
	if len(seen) < 2 {
		t.Fatalf("expected at least 2 distinct sampled values, got %d", len(seen))
	}
}

func TestTransport_ReceiveRespectsTimeout(t *testing.T) {
	inner := &fakeTransport{}
	w := Wrap(inner)
	tr := w.(*Transport)

	if err := tr.SetImpairment(map[string]string{"latency_ms": "500"}); err != nil {
		t.Fatalf("SetImpairment: %v", err)
	}

	start := time.Now()
	_, _, err := w.Receive(100 * time.Millisecond)
	elapsed := time.Since(start)

	if elapsed >= 250*time.Millisecond {
		t.Fatalf("Receive took %s, want < 250ms (not the full 600ms)", elapsed)
	}
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("expected errors.Is(err, os.ErrDeadlineExceeded), got %v", err)
	}
	if len(inner.recvTimes) != 0 {
		t.Fatalf("inner.Receive should never have been called, got %d calls", len(inner.recvTimes))
	}
}

func TestTransport_ReceivePassesRemainingBudgetToInner(t *testing.T) {
	inner := &fakeTransport{}
	w := Wrap(inner)
	tr := w.(*Transport)

	if err := tr.SetImpairment(map[string]string{"latency_ms": "60"}); err != nil {
		t.Fatalf("SetImpairment: %v", err)
	}

	if _, _, err := w.Receive(100 * time.Millisecond); err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if inner.lastTimeout <= 0 || inner.lastTimeout > 50*time.Millisecond {
		t.Fatalf("inner.Receive got timeout %s, want in (0, 50ms]", inner.lastTimeout)
	}
}

func TestTransport_ZeroProfileIsPassThrough(t *testing.T) {
	inner := &fakeTransport{}
	w := Wrap(inner)
	tr := w.(*Transport)

	if err := tr.SetImpairment(nil); err != nil {
		t.Fatalf("SetImpairment(nil): %v", err)
	}

	start := time.Now()
	if err := w.Send([]byte("x"), "h:1"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if elapsed := time.Since(start); elapsed >= 20*time.Millisecond {
		t.Fatalf("Send under zero profile took %s, want < 20ms", elapsed)
	}

	start = time.Now()
	if _, _, err := w.Receive(100 * time.Millisecond); err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if elapsed := time.Since(start); elapsed >= 20*time.Millisecond {
		t.Fatalf("Receive under zero profile took %s, want < 20ms", elapsed)
	}
	if inner.lastTimeout != 100*time.Millisecond {
		t.Fatalf("Receive under zero profile passed timeout %s, want unmodified 100ms", inner.lastTimeout)
	}
}

// TestTransport_SetImpairmentClearsLeakedArmedFlag is the unit-level guard
// for a coalesced-fast-path leak: a caller that arms Receive but never
// actually issues a Receive call (because it found what it needed some
// other way) must not have that armed delay silently fire on the next step.
func TestTransport_SetImpairmentClearsLeakedArmedFlag(t *testing.T) {
	inner := &fakeTransport{}
	w := Wrap(inner)
	tr := w.(*Transport)

	if err := tr.SetImpairment(map[string]string{"latency_ms": "200"}); err != nil {
		t.Fatalf("SetImpairment (arm): %v", err)
	}
	// Deliberately never call Receive here.
	if err := tr.SetImpairment(nil); err != nil {
		t.Fatalf("SetImpairment (clear): %v", err)
	}

	start := time.Now()
	if _, _, err := w.Receive(1 * time.Second); err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if elapsed := time.Since(start); elapsed >= 20*time.Millisecond {
		t.Fatalf("Receive after clearing leaked flag took %s, want < 20ms", elapsed)
	}
}

func TestTransport_LastDelaySumsSendAndReceive(t *testing.T) {
	inner := &fakeTransport{}
	w := Wrap(inner)
	tr := w.(*Transport)

	if err := tr.SetImpairment(map[string]string{"latency_ms": "100"}); err != nil {
		t.Fatalf("SetImpairment: %v", err)
	}
	if err := w.Send([]byte("x"), "h:1"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if _, _, err := w.Receive(5 * time.Second); err != nil {
		t.Fatalf("Receive: %v", err)
	}

	// Both Send and Receive were armed by the single SetImpairment call
	// above, so LastDelay must reflect both applied sleeps summed together.
	if got := tr.LastDelay(); got < 180*time.Millisecond {
		t.Fatalf("LastDelay = %s, want >= 180ms (sum of both ~100ms sleeps)", got)
	}
}

// TestWrap_PassesConnFaultThroughToInnerTransport is the real proof that a
// connection fault declared against a wrapped transport reaches the actual
// socket: it uses a real common.NewTCPTransport against a real listener,
// not the fakeTransport double, since the whole point is proving the fault
// lands on genuine connection bookkeeping.
func TestWrap_PassesConnFaultThroughToInnerTransport(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	accepted := make(chan struct{})
	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr == nil {
			defer conn.Close()
			buf := make([]byte, 64)
			conn.Read(buf) //nolint:errcheck
		}
		close(accepted)
	}()

	inner, err := common.NewTCPTransport(":0")
	if err != nil {
		t.Fatalf("NewTCPTransport: %v", err)
	}
	w := Wrap(inner)
	defer w.Close()

	addr := ln.Addr().String()
	connector := w.(common.Connector)
	if err := connector.Connect(addr); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := w.Send([]byte("first"), addr); err != nil {
		t.Fatalf("Send: %v", err)
	}
	<-accepted

	injector, ok := w.(common.ConnFaultInjector)
	if !ok {
		t.Fatalf("Wrap(tcpTransport) does not satisfy common.ConnFaultInjector")
	}
	if err := injector.ResetConn(); err != nil {
		t.Fatalf("ResetConn: %v", err)
	}

	err = w.Send([]byte("second"), addr)
	if err == nil {
		t.Fatalf("expected error after ResetConn, got nil")
	}
	if !strings.Contains(err.Error(), "is gone, reconnect required") {
		t.Fatalf("error %q does not name the connection as gone", err.Error())
	}
}

// TestWrap_ConnFaultOnUDPReportsUnsupported asserts the graceful runtime
// error path: a wrapped UDP transport satisfies common.ConnFaultInjector at
// compile time (the methods are declared unconditionally on *Transport),
// but calling one at runtime fails with a message naming the inner type.
func TestWrap_ConnFaultOnUDPReportsUnsupported(t *testing.T) {
	inner, err := common.NewUDPTransport(":0")
	if err != nil {
		t.Fatalf("NewUDPTransport: %v", err)
	}
	w := Wrap(inner)
	defer w.Close()

	injector, ok := w.(common.ConnFaultInjector)
	if !ok {
		t.Fatalf("Wrap(udpTransport) does not satisfy common.ConnFaultInjector")
	}
	err = injector.ResetConn()
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "UDPTransport") {
		t.Fatalf("error %q does not name the inner transport type", err.Error())
	}

	// SendTruncated, by contrast, must actually reach the inner UDP
	// transport: it is a TruncatingSender, unlike ConnFaultInjector.
	sender, ok := w.(common.TruncatingSender)
	if !ok {
		t.Fatalf("Wrap(udpTransport) does not satisfy common.TruncatingSender")
	}
	server, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}
	defer server.Close()
	if err := sender.SendTruncated([]byte("0123456789"), server.LocalAddr().String(), 4); err != nil {
		t.Fatalf("SendTruncated: %v", err)
	}
	buf := make([]byte, 64)
	server.SetReadDeadline(time.Now().Add(2 * time.Second)) //nolint:errcheck
	n, _, err := server.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("ReadFromUDP: %v", err)
	}
	if n != 4 {
		t.Fatalf("got %d bytes, want 4", n)
	}
}

// TestWrap_StillSplitsConnectorTypes is a regression guard for the
// invariant the connection-fault work must not break: adding ConnFaultInjector/
// TruncatingSender unconditionally to *Transport must not make a wrapped
// UDP transport falsely satisfy common.Connector.
func TestWrap_StillSplitsConnectorTypes(t *testing.T) {
	udpW := Wrap(&fakeTransport{})
	if _, ok := udpW.(common.Connector); ok {
		t.Fatalf("Wrap(non-connector transport) falsely satisfies common.Connector")
	}

	tcpW := Wrap(&fakeConnectorTransport{fakeTransport: &fakeTransport{}})
	if _, ok := tcpW.(common.Connector); !ok {
		t.Fatalf("Wrap(connector-capable transport) does not satisfy common.Connector")
	}
}

func TestProfileFromConfig_IgnoresConnectionFaultKeys(t *testing.T) {
	cfg := map[string]string{
		ResetConfigKey:       "true",
		TruncateConfigKey:    "true",
		AbruptCloseConfigKey: "true",
		SCTPAbortConfigKey:   "true",
	}
	got, err := ProfileFromConfig(cfg)
	if err != nil {
		t.Fatalf("ProfileFromConfig: %v", err)
	}
	if got != (Profile{}) {
		t.Fatalf("got %+v, want zero Profile", got)
	}

	inner := &fakeTransport{}
	w := Wrap(inner)
	tr := w.(*Transport)
	if err := tr.SetImpairment(cfg); err != nil {
		t.Fatalf("SetImpairment: %v", err)
	}
	if tr.sendArmed || tr.recvArmed {
		t.Fatalf("fault-only config armed a timing delay: sendArmed=%v recvArmed=%v", tr.sendArmed, tr.recvArmed)
	}
}
