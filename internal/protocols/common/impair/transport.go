// Package impair decorates a common.Transport with deliberate,
// scenario-declared delivery-timing impairment. It never inspects, parses,
// or builds protocol bytes: it only decides how long to sleep before
// handing a call through to the transport underneath.
package impair

import (
	"fmt"
	"math/rand"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
)

// Profile is the currently active send/receive timing impairment for one
// Transport instance. The zero Profile means "no impairment" — every field
// at its zero value must produce behaviour indistinguishable from an
// unwrapped transport.
type Profile struct {
	LatencyMs int
	JitterMs  int
}

// impairKeys maps each recognised config-map key to the Profile field it
// fills. Table-driven rather than a switch per CLAUDE.md, and the single
// place a new timing key gets registered.
var impairKeys = map[string]func(*Profile, int){
	"latency_ms": func(p *Profile, v int) { p.LatencyMs = v },
	"jitter_ms":  func(p *Profile, v int) { p.JitterMs = v },
}

// ProfileFromConfig parses the timing-impairment keys out of cfg, ignoring
// every other key the same way every config-map consumer in this codebase
// does (see internal/engine's timeout_ms/check handling). A nil or empty cfg
// produces the zero Profile. delay_ms, drop and duplicate are deliberately
// NOT among impairKeys — all three are adapter-owned delivery decisions,
// never transport-layer timing: delay_ms is post-receive delay (see
// helpers.go's PostReceiveDelay), and drop/duplicate decide whether a
// message reaches the wire at all or how many times. A transport-level drop
// would still leave the adapter waiting for an answer that will never come,
// which is exactly the hang this impairment exists to avoid — only the
// adapter knows it must skip the wait entirely. reset, truncate,
// abrupt_close and sctp_abort are likewise excluded: they are adapter-owned
// decisions about the connection and the message, dispatched through
// ApplyConnFault/SendMaybeTruncated (connfault.go), never transport-layer
// delivery timing.
func ProfileFromConfig(cfg map[string]string) (Profile, error) {
	var p Profile
	for key, apply := range impairKeys {
		raw, ok := cfg[key]
		if !ok {
			continue
		}
		v, err := strconv.Atoi(raw)
		if err != nil {
			return Profile{}, fmt.Errorf("impair: %s: %w", key, err)
		}
		if v < 0 {
			return Profile{}, fmt.Errorf("impair: %s must not be negative, got %d", key, v)
		}
		apply(&p, v)
	}
	return p, nil
}

// Transport decorates a common.Transport with deliberate delivery-timing
// impairment. Construct it with Wrap, never as a bare literal: Wrap is what
// decides whether the returned value should also carry Connect.
type Transport struct {
	inner common.Transport
	mu    sync.Mutex

	profile   Profile
	sendArmed bool
	recvArmed bool
	lastDelay time.Duration
}

// connectorTransport is Transport plus Connect, returned by Wrap only when
// the inner transport is itself a common.Connector. Two concrete types
// rather than one, because Go has no conditional method set: declaring
// Connect on Transport unconditionally would make a wrapped UDPTransport
// falsely satisfy common.Connector, and diameter.Adapter.Connect's type
// assertion would then hand a datagram socket to a connection-oriented
// handshake.
type connectorTransport struct {
	*Transport
	connector common.Connector
}

func (c *connectorTransport) Connect(addr string) error { return c.connector.Connect(addr) }

// Wrap returns tr decorated with impairment control. The result is a no-op
// pass-through until SetImpairment arms it, so wrapping unconditionally at
// adapter-construction time costs nothing for scenarios that declare no
// impairment.
func Wrap(tr common.Transport) common.Transport {
	t := &Transport{inner: tr}
	if c, ok := tr.(common.Connector); ok {
		return &connectorTransport{Transport: t, connector: c}
	}
	return t
}

// Close delegates to the inner transport.
func (t *Transport) Close() error {
	return t.inner.Close()
}

// LocalAddr delegates to the inner transport.
func (t *Transport) LocalAddr() net.Addr {
	return t.inner.LocalAddr()
}

// SetImpairment implements common.ImpairmentControl. It replaces the
// currently active Profile and re-arms both one-shot delay flags: a
// non-zero profile means the very next Send and the very next Receive each
// owe their delay, and any leftover armed state from a previous step is
// cleared here even when the new profile is itself zero.
func (t *Transport) SetImpairment(cfg map[string]string) error {
	p, err := ProfileFromConfig(cfg)
	if err != nil {
		return err
	}

	armed := p.LatencyMs > 0 || p.JitterMs > 0

	t.mu.Lock()
	defer t.mu.Unlock()
	t.profile = p
	t.sendArmed = armed
	t.recvArmed = armed
	t.lastDelay = 0
	return nil
}

// LastDelay reports how much delay this Transport actually applied since
// the last SetImpairment call, summing armed Send and Receive sleeps. It is
// deliberately NOT part of common.Transport — adapters reach it by type
// assertion, since it is an observability detail, not part of the wire
// contract every transport must satisfy.
func (t *Transport) LastDelay() time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lastDelay
}

// sample draws this call's delay: the fixed latency plus a uniform draw in
// [0, JitterMs]. Caller must hold t.mu.
func (t *Transport) sample() time.Duration {
	ms := t.profile.LatencyMs
	if t.profile.JitterMs > 0 {
		ms += rand.Intn(t.profile.JitterMs + 1)
	}
	return time.Duration(ms) * time.Millisecond
}

// takeArmed consumes one of the two one-shot arming flags and returns the
// delay this call must apply (0 when not armed). Named helper rather than
// an inline `pass`-style flag in both Send and Receive, per CLAUDE.md.
func (t *Transport) takeArmed(armed *bool) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !*armed {
		return 0
	}
	*armed = false
	return t.sample()
}

// addDelay accumulates d into the running total LastDelay reports.
func (t *Transport) addDelay(d time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lastDelay += d
}

// Send applies this step's outbound latency once, then delegates. Both
// adapters call Send exactly once per step, so "once per SetImpairment" and
// "once per step" are the same thing on the send side.
func (t *Transport) Send(msg []byte, addr string) error {
	if delay := t.takeArmed(&t.sendArmed); delay > 0 {
		time.Sleep(delay)
		t.addDelay(delay)
	}
	return t.inner.Send(msg, addr)
}

// Receive applies this step's inbound delay at most once, then delegates
// with whatever budget is left.
//
// Two invariants live here. First, the delay is one-shot: a Diameter answer
// assembled from three physical reads, or a SIP final response preceded by
// two 1xx provisionals, pays the injected delay once for the message, not
// once per read — a per-call sleep would multiply the delay by however many
// chunks the OS happened to deliver, which is not a bound any test can
// assert. Second, the sleep is clamped to the caller's own remaining
// budget: a caller that computes its deadline once and never re-arms it
// would otherwise have an unclamped sleep here silently push a step past
// the timeout the scenario asked for.
func (t *Transport) Receive(timeout time.Duration) ([]byte, net.Addr, error) {
	delay := t.takeArmed(&t.recvArmed)
	if delay == 0 {
		return t.inner.Receive(timeout)
	}
	wait := min(delay, timeout)
	time.Sleep(wait)
	t.addDelay(wait)
	if delay >= timeout {
		return nil, nil, fmt.Errorf("impair: injected delay %s consumed the whole %s budget: %w", delay, timeout, os.ErrDeadlineExceeded)
	}
	return t.inner.Receive(timeout - wait)
}

// injectFault delegates one ConnFaultInjector method to the inner transport,
// or returns a descriptive error when the inner transport has no connection
// lifecycle to fault.
//
// Unlike Connect, these methods are declared on Transport unconditionally.
// connectorTransport exists because a wrapped UDPTransport falsely
// satisfying common.Connector would hand a datagram socket to Diameter's
// connection-oriented handshake — a silent, wrong success. Here the opposite
// is true: `impair: { reset: true }` on SIP-over-UDP SHOULD fail the step
// with a message naming why, and a runtime error carries that message where
// a failed type assertion could only produce a generic one.
func (t *Transport) injectFault(name string, method func(common.ConnFaultInjector) error) error {
	inner, ok := t.inner.(common.ConnFaultInjector)
	if !ok {
		return fmt.Errorf("impair: %s: transport %T has no connection to fault", name, t.inner)
	}
	return method(inner)
}

// ResetConn, AbruptCloseConn and AbortConn implement common.ConnFaultInjector
// by delegation. They apply no delay of their own: a connection fault is not
// a timing impairment, and this decorator's one-shot arming belongs entirely
// to Send/Receive.
func (t *Transport) ResetConn() error {
	return t.injectFault("reset", common.ConnFaultInjector.ResetConn)
}

func (t *Transport) AbruptCloseConn() error {
	return t.injectFault("abrupt_close", common.ConnFaultInjector.AbruptCloseConn)
}

func (t *Transport) AbortConn() error {
	return t.injectFault("sctp_abort", common.ConnFaultInjector.AbortConn)
}

// SendTruncated implements common.TruncatingSender by delegation. It
// deliberately does NOT consume this step's armed send-side delay: a
// truncated send is one write, and charging it a latency the ordinary Send
// path would also charge would double-count if a future step ever did both.
// The step's declared latency is applied by Send, which a truncated step
// does not call.
func (t *Transport) SendTruncated(msg []byte, addr string, n int) error {
	ts, ok := t.inner.(common.TruncatingSender)
	if !ok {
		return fmt.Errorf("impair: truncate: transport %T does not support short writes", t.inner)
	}
	return ts.SendTruncated(msg, addr, n)
}

var (
	_ common.Transport         = (*Transport)(nil)
	_ common.ImpairmentControl = (*Transport)(nil)
	_ common.ConnFaultInjector = (*Transport)(nil)
	_ common.TruncatingSender  = (*Transport)(nil)
	_ common.Transport         = (*connectorTransport)(nil)
	_ common.Connector         = (*connectorTransport)(nil)
)
