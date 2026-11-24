package diameter

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
)

// session.go is the receive half of one held Diameter connection: a byte
// stream framer (nextMessage/messageLength) plus a pair of demultiplexing
// wait loops — receiveAnswer (single Hop-by-Hop ID) and receiveAnswers
// (a whole set, for pipelined/reordered groups) — that each answer any
// inbound DWR they meet while waiting.
//
// This file deliberately never starts a goroutine and never calls
// common.Transport.Receive from more than one place at a time: the
// transport is not safe for concurrent Send+Receive pairs from multiple
// goroutines against one held connection. Both receiveAnswer and
// receiveAnswers hold that invariant by construction — each is a single
// synchronous loop on the caller's own goroutine — so no lock is needed
// to enforce it.
//
// Why this framing lives here and not in common.readFrom: readFrom
// (internal/protocols/common/transport.go) is shared with SIP, and SIP
// frames its own messages differently (one UDP datagram is one SIP
// message). Changing readFrom to understand Diameter's Message Length
// header would put the one protocol that already works at risk to fix a
// Diameter problem. This is also not a
// mock-only concern: a real peer (Open5GS) coalesces and splits
// exactly like TCP is allowed to, so this framer is production logic, not
// test scaffolding.
//
// On messageLength vs. testing.go's extractMessage: both solve "does buf
// hold a complete Diameter message yet", and both were written against the
// same Message Length header. They are kept SEPARATE on purpose rather than
// shared, because their jobs are not actually identical once you look past
// the shared header math:
//
//   - messageLength only answers "how many bytes does the header claim, and
//     do we have them yet" — it does not parse and does not validate. This
//     plan's tryExtract layers version validation and a size cap (
//     maxSessionMessageSize) on top, because nextMessage is production code
//     receiving from a real peer over the network and must reject garbage
//     input loudly rather than block forever waiting for bytes a corrupt
//     length field promises but which will never arrive.
//   - extractMessage (testing.go) trusts the peer implicitly — it is the
//     mock HSS's OWN read loop, framing bytes this same test binary just
//     wrote moments earlier on the other end of an in-process socket. It
//     has no version check and no size cap because a test process attacking
//     itself is not a threat model worth coding against.
//
// nextMessage and extractMessage (testing.go) are not one shared helper
// because they answer to different threat models: nextMessage must validate
// version and size against a possibly hostile real peer, whereas
// extractMessage frames bytes this same test binary just wrote moments
// earlier over an in-process socket. Reusing extractMessage's signature
// directly was rejected because it does not give nextMessage the
// version/size validation TestSession_NextMessage_RejectsGarbage and
// TestSession_NextMessage_RejectsOversizedLength require.

// maxSessionMessageSize bounds how large a single buffered Diameter message
// may declare itself to be. Without a cap, a corrupt or hostile Message
// Length field could make session.buf grow without bound while nextMessage
// keeps waiting for bytes that will never arrive. 65535 matches
// common.maxMessageSize's role for the transport's own per-Read buffer; the
// two are independent constants because this package does not import an
// unexported constant from common.
const maxSessionMessageSize = 65535

// errAnswerTimeout is receiveAnswer's own timeout, wrapped with the
// Hop-by-Hop ID it was waiting for. It is deliberately distinct from any
// error a Transport.Receive call might itself return (e.g.
// os.ErrDeadlineExceeded from a single Receive, or a dead-connection error)
// so a caller — and a test — can tell "the whole wait budget expired" apart
// from "the transport itself failed."
var errAnswerTimeout = errors.New("diameter session: timed out waiting for answer")

// session is the receive half of one held Diameter connection. TCP is a
// byte stream, not a message queue: a peer may pack a DWR and an answer
// into one segment, or split one message across two. buf holds bytes read
// but not yet consumed by a complete message.
type session struct {
	tr     common.Transport
	target string
	host   string // Origin-Host for the DWAs this session emits
	realm  string // Origin-Realm
	buf    []byte
}

// messageLength reads the 3-byte Message Length header (RFC 6733 §3, bytes
// [1:4]) out of buf and reports whether buf already holds that many bytes.
// Callers must ensure len(buf) >= HeaderLength before calling — a length
// shorter than the header cannot be decoded and this function does not
// re-check it.
func messageLength(buf []byte) (int, bool) {
	msgLen := int(buf[1])<<16 | int(buf[2])<<8 | int(buf[3])
	return msgLen, len(buf) >= msgLen
}

// tryExtract attempts to pull one complete, validated message out of
// s.buf without touching the transport. ok is true only when a message was
// extracted (and s.buf advanced past it); a non-nil err means the buffered
// bytes are unrecoverably invalid (bad version, an impossible length) and
// must not be treated as "need more data."
func (s *session) tryExtract() ([]byte, *Message, bool, error) {
	if len(s.buf) < HeaderLength {
		return nil, nil, false, nil
	}
	if s.buf[0] != Version {
		return nil, nil, false, fmt.Errorf("diameter session: unsupported version %d", s.buf[0])
	}
	msgLen, complete := messageLength(s.buf)
	if msgLen < HeaderLength {
		return nil, nil, false, fmt.Errorf("diameter session: invalid message length %d", msgLen)
	}
	if msgLen > maxSessionMessageSize {
		return nil, nil, false, fmt.Errorf("diameter session: message length %d exceeds max %d", msgLen, maxSessionMessageSize)
	}
	if !complete {
		return nil, nil, false, nil
	}

	raw := s.buf[:msgLen]
	msg, err := ParseMessage(raw)
	if err != nil {
		return nil, nil, false, err
	}
	s.buf = s.buf[msgLen:]
	return raw, msg, true, nil
}

// nextMessage returns a complete message out of buf WITHOUT calling Receive
// when one is already buffered (the coalescing case — two messages arrived
// in a prior Read). Otherwise it calls s.tr.Receive repeatedly, appending
// to buf, until a complete message is available (the splitting case — one
// message arrives across multiple Reads) or the timeout/an error ends the
// wait. timeout is a budget for THIS call only; the caller (receiveAnswer)
// is responsible for not re-arming it across multiple
// nextMessage calls.
func (s *session) nextMessage(timeout time.Duration) ([]byte, *Message, error) {
	if raw, msg, ok, err := s.tryExtract(); ok || err != nil {
		return raw, msg, err
	}

	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining < 0 {
			remaining = 0
		}
		data, _, err := s.tr.Receive(remaining)
		if err != nil {
			return nil, nil, err
		}
		s.buf = append(s.buf, data...)

		if raw, msg, ok, err := s.tryExtract(); ok || err != nil {
			return raw, msg, err
		}
	}
}

// receiveAnswer waits for the answer to hopByHop, transparently answering
// any inbound DWR seen while waiting (RFC 6733 §5.5) and discarding anything
// that does not correlate by Hop-by-Hop ID. deadline is computed ONCE by the caller and
// never re-armed here: a chatty peer sending DWRs faster than the budget
// would keep the wait alive forever if the deadline were re-armed per DWR.
//
// Correlation here is a single Hop-by-Hop ID threaded through the call, not
// a map: this client has at most one outstanding request at a time, so
// "outstanding request" is one uint32, not a set. This is a decision, not
// an oversight — it is what makes the demux inline here rather than
// dispatched through a lookup table. The pipelined case is handled by the
// plural sibling receiveAnswers below, not by adding a second behaviour
// here: a per-ID channel guarded by its own mutex would assume concurrent
// producers and consumers, which would need a background reader goroutine
// racing this file's deliberate single-reader design. receiveAnswers
// instead buffers locally on the same synchronous call stack, so it needs
// no channel and no mutex.
func (s *session) receiveAnswer(hopByHop uint32, deadline time.Time) (*Message, []byte, error) {
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, nil, fmt.Errorf("%w %d", errAnswerTimeout, hopByHop)
		}

		raw, msg, err := s.nextMessage(remaining)
		if err != nil {
			return nil, nil, err
		}

		if msg.CommandCode == CmdDeviceWatchdog && msg.IsRequest() {
			if err := s.answerDWR(msg); err != nil {
				return nil, nil, err
			}
			continue
		}
		if msg.HopByHopID != hopByHop {
			continue
		}
		return msg, raw, nil
	}
}

// answerSet maps a Hop-by-Hop ID to the answer received for it. It is the
// return shape of a group wait: the caller sent N requests and gets back a
// lookup keyed by the identifier it already holds for each one, so
// correlation never depends on arrival order or on slice position.
type answerSet map[uint32]*Message

// rawAnswerSet maps a Hop-by-Hop ID to that answer's exact wire bytes,
// parallel to answerSet. Kept separate rather than folded into a struct
// because StepResult.RawResponse and StepResult.Response are populated from
// the two independently, exactly as buildStepResult already takes
// (respMsg, respRaw) as two arguments.
type rawAnswerSet map[uint32][]byte

// wantedIDs turns a Hop-by-Hop ID slice into the lookup receiveAnswers
// checks each inbound message against.
func wantedIDs(hopByHops []uint32) map[uint32]bool {
	want := make(map[uint32]bool, len(hopByHops))
	for _, h := range hopByHops {
		want[h] = true
	}
	return want
}

// receiveAnswers waits for answers to every ID in hopByHops, in ANY arrival
// order, without discarding an answer that belongs to this group but does
// not happen to be the one checked next. It exists because receiveAnswer
// cannot do this: that method discards every non-matching message
// (`if msg.HopByHopID != hopByHop { continue }`) after tryExtract has
// already advanced s.buf past it, so the answer and its bytes are gone for
// good. Calling receiveAnswer N times for a group of N pipelined requests
// therefore loses an answer whenever wire order differs from read order.
//
// deadline is a single absolute deadline covering the WHOLE group,
// computed once by the caller and never re-armed here — receiveAnswer's own
// never-re-armed convention, extended to a set, so a chatty peer sending
// DWRs cannot keep the group alive forever.
//
// This adds no concurrency. The buffering is local to this call's stack,
// not a field on session: it cannot persist across steps, cannot leak state
// between an ordinary send and a later group wait, and needs no clearing
// logic anywhere. There is still exactly one goroutine calling
// s.tr.Receive, via the same s.nextMessage framer receiveAnswer uses — this
// method adds no framing logic of its own and therefore inherits the
// whole-framed-message guarantee rather than re-deriving it.
//
// An inbound message whose Hop-by-Hop ID is NOT in hopByHops is discarded
// exactly as receiveAnswer already discards a stray or duplicate answer.
// This method only changes behaviour for IDs it was explicitly told to
// await.
//
// On timeout or transport error the partial maps collected so far are
// returned alongside the error, so a caller can report per-request outcomes
// ("request 2 was answered, request 1 timed out") instead of failing a
// whole group opaquely.
func (s *session) receiveAnswers(hopByHops []uint32, deadline time.Time) (answerSet, rawAnswerSet, error) {
	want := wantedIDs(hopByHops)
	msgs := make(answerSet, len(want))
	raws := make(rawAnswerSet, len(want))

	for len(msgs) < len(want) {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return msgs, raws, fmt.Errorf("%w: group received %d of %d", errAnswerTimeout, len(msgs), len(want))
		}
		raw, msg, err := s.nextMessage(remaining)
		if err != nil {
			return msgs, raws, err
		}
		if msg.CommandCode == CmdDeviceWatchdog && msg.IsRequest() {
			if err := s.answerDWR(msg); err != nil {
				return msgs, raws, err
			}
			continue
		}
		if !want[msg.HopByHopID] {
			continue // not this group's — same discard receiveAnswer already does
		}
		msgs[msg.HopByHopID] = msg
		raws[msg.HopByHopID] = raw
	}
	return msgs, raws, nil
}

// answerDWR builds a DWA answering an inbound DWR: Result-Code 2001,
// Origin-Host/Origin-Realm, R-bit clear (BuildAnswer) and P-bit never set —
// a DWA with the R-bit or P-bit set draws INVALID_HDR_BITS (3008) from real
// peers on command codes 257/280. copyAnswerIdentifiers is this session's
// own production logic, not a reuse of testing.go's mock-only
// copyIdentifiers helper: this session answers a possibly hostile real
// peer, while the mock's copyIdentifiers frames bytes this same test binary
// just wrote over an in-process socket — different threat models is why
// they are not one shared helper.
func (s *session) answerDWR(req *Message) error {
	avps := []AVP{
		NewUint32AVP(AVPCodeResultCode, ResultSuccess),
		NewStringAVP(AVPCodeOriginHost, s.host),
		NewStringAVP(AVPCodeOriginRealm, s.realm),
	}
	raw, err := BuildAnswer(CmdDeviceWatchdog, 0, avps)
	if err != nil {
		return fmt.Errorf("diameter session: build DWA: %w", err)
	}
	raw = copyAnswerIdentifiers(raw, req)
	return s.tr.Send(raw, s.target)
}

// copyAnswerIdentifiers overwrites answer's Hop-by-Hop and End-to-End
// fields (bytes [12:20]) with req's, as RFC 6733 requires an answer to echo
// its request's identifiers.
func copyAnswerIdentifiers(answer []byte, req *Message) []byte {
	binary.BigEndian.PutUint32(answer[12:16], req.HopByHopID)
	binary.BigEndian.PutUint32(answer[16:20], req.EndToEndID)
	return answer
}
