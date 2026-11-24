// reorder.go implements common.ReorderSender for SIP.
//
// The design starts from the same place as the Diameter implementation —
// build every member's message first, then write in ascending SendOrder,
// from one goroutine — but the READ half cannot mirror Diameter's, and that
// difference is this file's whole reason for existing.
//
// SIP's response correlation here is POSITIONAL, not identifier-matched.
// This adapter performs no request/response correlation at all:
// waitForFinalResponse returns whatever the next Receive produces, checking
// only that the status is >= 200. It does not compare Call-ID, CSeq, or the
// Via branch against the request that was just sent — not for a reorder
// group, and not for an ordinary single send either. There is consequently
// nothing to match a response against, so the ONLY way to attribute a
// response to its originating member is to read responses in the exact order
// the requests were physically written, and trust the peer to answer in
// receipt order.
//
// That trust is sound against this repository's own mocks, which are
// single-goroutine, strictly synchronous read-then-respond loops and cannot
// answer a later request before an earlier one. It is NOT a guarantee about
// real peers. A UAS that processed an INVITE asynchronously while answering
// a concurrent OPTIONS immediately would make this code attribute responses
// to the wrong members, silently, with no way to detect it.
//
// This is a real, inherent limitation of SIP's current transport model in
// this codebase — not a defect introduced by reordering, and explicitly NOT
// equivalent to the Diameter implementation's Hop-by-Hop-ID correlation,
// which matches every answer to its own request regardless of arrival order,
// including a duplicate's stray second answer (an already-satisfied
// Hop-by-Hop ID is simply discarded, by pre-existing behaviour, whenever it
// turns up). SIP has no such safety net, which is why this file pairs each
// member's write with its OWN read — including draining a duplicated
// member's echo — before the NEXT member's request ever reaches the wire:
// writing every member up front and only then reading them all back would
// let a later member's genuine answer already be sitting in the socket
// buffer by the time an earlier duplicated member's echo is drained, and the
// drain has no identifier to tell the two apart. Building genuine
// Call-ID/CSeq correlation for SIP is a larger change affecting every SIP
// step, not just this one, and is deliberately out of this feature's scope.
//
// As on the Diameter side: no goroutine is started here, and every
// operation is on a whole, already-serialised message — never a byte range
// smaller than one complete message.
package sip

import (
	"fmt"
	"sort"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/protocols/common/impair"
)

var _ common.ReorderSender = (*Adapter)(nil)

// builtSIPRequest is one member's serialised request plus the index of the
// member that produced it. The index travels with the bytes because the
// exchange phase walks members in SendOrder while results must come back in
// declaration order.
type builtSIPRequest struct {
	index  int
	raw    []byte
	member common.ReorderMember
}

// ExecuteReorder implements common.ReorderSender: build every member's
// request first (declaration order), then run one member's full exchange —
// write, then read its own response, POSITIONALLY (see the package doc) —
// at a time, in ascending SendOrder, before the next member's request ever
// reaches the wire. Results are returned in declaration order.
func (a *Adapter) ExecuteReorder(members common.ReorderMembers, timeoutMs int) ([]*common.StepResult, error) {
	start := time.Now()

	built, failure := a.buildSIPReorderRequests(members)
	if failure != nil {
		return reorderGroupFailure(len(members), failure), nil
	}

	// Computed once, before a single byte goes out, and never re-armed
	// afterward — the same never-re-armed convention waitForFinalResponse
	// already applies to a single request, extended here to the whole group.
	deadline := a.reorderDeadline(timeoutMs)
	ordered := sortSIPReorderBySendOrder(built)

	results := make([]*common.StepResult, len(members))
	if err := a.runSIPReorderExchanges(start, ordered, results, deadline); err != nil {
		return nil, err
	}
	return results, nil
}

// reorderDeadline computes the group's ONE absolute deadline. It is both
// checked BETWEEN members (so a member is never started after the budget is
// gone) and passed INTO waitForFinalResponse for each member's read, so a
// single slow member cannot overrun the group's total budget either.
func (a *Adapter) reorderDeadline(timeoutMs int) time.Time {
	budget := a.timeout
	if timeoutMs > 0 {
		budget = time.Duration(timeoutMs) * time.Millisecond
	}
	return time.Now().Add(budget)
}

// buildSIPReorderRequests serialises every member's request in declaration
// order, using the same buildRequestMessage/Serialise path an ordinary send
// step uses. Any build failure aborts the whole group: a partially-sent
// group produces a wire trace no scenario author could reason about, so
// nothing here is written to the wire before every member has built
// successfully.
func (a *Adapter) buildSIPReorderRequests(members common.ReorderMembers) ([]builtSIPRequest, *common.StepResult) {
	built := make([]builtSIPRequest, 0, len(members))
	for i, m := range members {
		msg, failure := a.buildRequestMessage(m.Target, m.Message, m.Fields)
		if failure != nil {
			failure.Error = fmt.Sprintf("reorder member %d failed to build: %s", i, failure.Error)
			return nil, failure
		}
		built = append(built, builtSIPRequest{index: i, raw: Serialise(msg), member: m})
	}
	return built, nil
}

// reorderGroupFailure returns n copies of failure, one per declared member —
// a whole-group build failure has no single member to attribute the result
// to, so every member's own result names the same failure.
func reorderGroupFailure(n int, failure *common.StepResult) []*common.StepResult {
	results := make([]*common.StepResult, n)
	for i := range results {
		results[i] = failure
	}
	return results
}

// sortSIPReorderBySendOrder returns a COPY of built ordered by ascending
// SendOrder. built itself is left untouched (unused after this call).
// sort.SliceStable degrades two members declaring the same SendOrder — which
// the validator already rejects, but which a direct API caller could still
// pass — to declaration order rather than to something arbitrary.
func sortSIPReorderBySendOrder(built []builtSIPRequest) []builtSIPRequest {
	ordered := make([]builtSIPRequest, len(built))
	copy(ordered, built)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].member.SendOrder < ordered[j].member.SendOrder
	})
	return ordered
}

// runSIPReorderExchanges walks ordered (ascending SendOrder) and, for each
// member, completes that member's ENTIRE exchange — write, then read its own
// response, then drain a duplicated member's echo — before the next
// member's write ever happens.
//
// This tight pairing, rather than a write-everybody-then-read-everybody
// split, is what makes duplicate:true safe to compose inside a group under
// POSITIONAL correlation: the next member's request has not yet reached the
// wire while an earlier duplicated member's echo is being drained, so there
// is nothing else already sitting in the socket for that drain to
// over-consume. A member declaring drop never reaches the wire and consumes
// no time, so it is filled in and skipped regardless of where in send_order
// it falls — including after the group deadline has already passed.
func (a *Adapter) runSIPReorderExchanges(start time.Time, ordered []builtSIPRequest, results []*common.StepResult, deadline time.Time) error {
	for i, b := range ordered {
		if impair.DropRequested(b.member.Config) {
			results[b.index] = impair.DroppedResult(start)
			continue
		}
		if !time.Now().Before(deadline) {
			a.fillSIPReorderTimeouts(start, ordered[i:], results)
			return nil
		}
		if err := a.sendSIPReorderMember(b); err != nil {
			return err
		}
		a.readSIPReorderMember(start, b, results, deadline)
	}
	return nil
}

// sendSIPReorderMember arms this member's own impairment then writes its
// bytes once, or twice at this same position when its config declares
// duplicate:true. Only the first Send pays any declared latency:
// impair.Configure's one-shot arming is consumed by that first Send, and
// this second Send fires immediately afterward — the same one-shot-arming
// consequence the standalone duplicate path already relies on.
func (a *Adapter) sendSIPReorderMember(b builtSIPRequest) error {
	if err := impair.Configure(a.transport, b.member.Config); err != nil {
		return err
	}
	if err := a.transport.Send(b.raw, b.member.Target); err != nil {
		return err
	}
	if impair.DuplicateRequested(b.member.Config) {
		if err := a.transport.Send(b.raw, b.member.Target); err != nil {
			return err
		}
	}
	return nil
}

// fillSIPReorderTimeouts fills every remaining member's slot once the group
// deadline has passed, rather than starting another read that has no budget
// left. A member declaring drop still reports dropped, not timed out: drop
// never touches the wire, so the group deadline elapsing has nothing to do
// with it.
func (a *Adapter) fillSIPReorderTimeouts(start time.Time, remaining []builtSIPRequest, results []*common.StepResult) {
	for _, b := range remaining {
		if impair.DropRequested(b.member.Config) {
			results[b.index] = impair.DroppedResult(start)
			continue
		}
		results[b.index] = sipReorderFailure(start, b.index, "group timeout elapsed before this member's response was read")
	}
}

// readSIPReorderMember reads this member's own final response POSITIONALLY
// — the next thing to come off the wire, per this file's package doc — and,
// for a duplicated member, drains its stray second response before
// returning, so the echo cannot be misread as the NEXT member's response
// once that member's own request goes out. This is the group-level instance
// of the hazard the standalone duplicate path's drainDuplicateEcho already
// closes; inside a group the consequence is worse, since an undrained echo
// would be attributed to a different sub-step of the SAME step, not merely a
// later, unrelated step.
func (a *Adapter) readSIPReorderMember(start time.Time, b builtSIPRequest, results []*common.StepResult, deadline time.Time) {
	// wantCSeq 0: a reorder member reads POSITIONALLY by design (see this
	// file's package doc), so no CSeq filter applies. Reorder members are
	// also deliberately NOT given the digest retry: a second send and read
	// here would break the group's positional write/read ordering.
	respMsg, respRaw, errResult := a.waitForFinalResponse(start, deadline, 0)
	if errResult != nil {
		results[b.index] = sipReorderFailure(start, b.index, errResult.Error)
	} else {
		results[b.index] = a.finishSIPReorderMember(start, b, respRaw, respMsg)
	}

	if impair.DuplicateRequested(b.member.Config) {
		a.drainDuplicateEcho()
	}
}

// finishSIPReorderMember builds one member's own passed StepResult, stamping
// duplicated and any declared per-member post-receive delay — impairment is
// declared per sub-step, never per group.
func (a *Adapter) finishSIPReorderMember(start time.Time, b builtSIPRequest, respRaw []byte, respMsg *SIPMessage) *common.StepResult {
	// Routed through the same status gate as an ordinary send, so a group
	// member answered 500 fails exactly as a standalone step would. This
	// used to build a StatusPassed literal of its own and was missed when
	// the gate first landed on finishSend.
	result := finalResponseResult(start, respRaw, respMsg)
	if _, challenged := challengeHeaders[respMsg.StatusCode]; challenged && result.Status == common.StatusFailed {
		result.Error += "; this reorder member was challenged and digest authentication is not attempted inside a reorder group"
	}
	if impair.DuplicateRequested(b.member.Config) {
		result = impair.MarkDuplicated(result)
	}
	if postDelay := impair.PostReceiveDelay(b.member.Config); postDelay > 0 {
		if result.Response == nil {
			result.Response = common.ResponseFields{}
		}
		result.Response[impair.ImpairDelayKey] = postDelay.Milliseconds()
	}
	return result
}

// sipReorderFailure names which member failed inside a failed StepResult,
// extracted per CLAUDE.md's no-copy-pasted-error-blocks rule.
func sipReorderFailure(start time.Time, index int, msg string) *common.StepResult {
	return &common.StepResult{
		Status:     common.StatusFailed,
		DurationMs: time.Since(start).Milliseconds(),
		Error:      fmt.Sprintf("reorder member %d: %s", index, msg),
	}
}
