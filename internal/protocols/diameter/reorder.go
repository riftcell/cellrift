// reorder.go implements common.ReorderSender for Diameter: a batch of send
// sub-steps whose wire order is declared independently of the order the
// scenario lists them in.
//
// Two properties are load-bearing and neither may be weakened.
//
// First, no new concurrency. Every message is built before any is written,
// so reordering the WRITE order is a buffer-then-flush inside one
// sequential call path — not a producer/consumer problem. Nothing here
// starts a goroutine, and the single-reader design the receive half depends
// on is untouched.
//
// Second, the group's answers are collected by session.receiveAnswers, the
// plural method, in ONE blocking call. Calling the singular receiveAnswer
// once per member in declaration order is a silent data-loss bug, not a
// style choice: that method discards any answer whose Hop-by-Hop ID is not
// the one it is currently waiting for, after the framer has already
// consumed the bytes, so an answer arriving out of declaration order is
// gone for good.
//
// Everything here operates on whole, already-framed messages produced by
// BuildRequest. No byte range smaller than one complete message is ever
// manipulated, so Diameter's length-field framing cannot be corrupted by
// reordering.
package diameter

import (
	"fmt"
	"sort"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/protocols/common/impair"
)

var _ common.ReorderSender = (*Adapter)(nil)

// builtRequest is one member's request after building: its wire bytes, its
// Hop-by-Hop ID, and the index of the member that produced it. The index
// travels with the request because the write phase sorts by SendOrder while
// results must be returned in declaration order — without carrying the
// original index, the two orderings could not be reconciled.
type builtRequest struct {
	index    int
	raw      []byte
	hopByHop uint32
	member   common.ReorderMember
}

// ExecuteReorder implements common.ReorderSender: build every member's
// request first (declaration order), write the built requests in ascending
// SendOrder, then collect every answer in one Hop-by-Hop-correlated group
// wait, returning results in declaration order. See this file's package
// comment for the two invariants it holds.
func (a *Adapter) ExecuteReorder(members common.ReorderMembers, timeoutMs int) ([]*common.StepResult, error) {
	start := time.Now()

	built, failure, err := a.buildReorderRequests(start, members)
	if err != nil {
		return nil, err
	}
	if failure != nil {
		return reorderGroupFailure(len(members), failure), nil
	}

	// Computed before the first byte goes out — see reorderDeadline.
	deadline := a.reorderDeadline(timeoutMs)

	hopByHops, err := a.writeReorderRequests(built)
	if err != nil {
		return nil, err
	}

	if len(hopByHops) == 0 {
		// Every member declared drop: nothing was written, so there is
		// nothing to wait for. Blocking the group's full timeout here would
		// be the exact hang impair.DropRequested's single-step short-circuit
		// already avoids.
		return reorderResults(start, built, nil, nil, nil), nil
	}

	msgs, raws, waitErr := a.sess.receiveAnswers(hopByHops, deadline)
	return reorderResults(start, built, msgs, raws, waitErr), nil
}

// reorderDeadline computes the group's ONE absolute deadline, before the
// first byte goes out and never re-armed afterward — extending
// receiveAnswer's own never-re-armed convention from one request to the
// whole set, so a chatty peer cannot keep the group alive forever by
// re-arming a per-member deadline.
func (a *Adapter) reorderDeadline(timeoutMs int) time.Time {
	budget := a.timeout
	if timeoutMs > 0 {
		budget = time.Duration(timeoutMs) * time.Millisecond
	}
	return time.Now().Add(budget)
}

// buildReorderRequests builds every member's request in DECLARATION order.
// The first call runs the CER/CEA handshake via prepareRequest's
// ensureConnected; later calls no-op on it. A single build failure aborts
// the whole group: sending some of N declared messages while silently
// omitting others produces a wire trace no scenario author could reason
// about, and a partially-executed group is worse than a failed one.
func (a *Adapter) buildReorderRequests(start time.Time, members common.ReorderMembers) ([]builtRequest, *common.StepResult, error) {
	built := make([]builtRequest, 0, len(members))
	for i, m := range members {
		raw, hopByHop, failure, err := a.prepareRequest(start, m.Target, m.Message, m.Fields, m.Config)
		if err != nil {
			return nil, nil, err
		}
		if failure != nil {
			return nil, reorderBuildFailure(i, failure), nil
		}
		built = append(built, builtRequest{index: i, raw: raw, hopByHop: hopByHop, member: m})
	}
	return built, nil, nil
}

// reorderBuildFailure names which member failed to build inside the one
// failure result every member of an aborted group ends up reporting.
func reorderBuildFailure(index int, failure *common.StepResult) *common.StepResult {
	failure.Error = fmt.Sprintf("reorder member %d failed to build: %s", index, failure.Error)
	return failure
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

// writeReorderRequests writes the built requests in ascending SendOrder and
// returns the Hop-by-Hop IDs the group must now wait for. A member
// declaring drop contributes nothing to the wire and nothing to the wait
// set. impair.Configure is called per member, not per group: latency,
// jitter, drop and duplicate are declared per sub-step, exactly like an
// ordinary send step's own impair: block.
//
// Sorting happens on a COPY of built: built itself stays in declaration
// order for buildReorderRequests'/reorderResults' index bookkeeping, and
// sort.SliceStable degrades two members declaring the same SendOrder — which
// the validator already rejects, but which a direct API caller could still
// pass — to declaration order rather than to something arbitrary.
func (a *Adapter) writeReorderRequests(built []builtRequest) ([]uint32, error) {
	ordered := make([]builtRequest, len(built))
	copy(ordered, built)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].member.SendOrder < ordered[j].member.SendOrder
	})

	hopByHops := make([]uint32, 0, len(ordered))
	for _, b := range ordered {
		if impair.DropRequested(b.member.Config) {
			continue
		}
		if err := a.sendMember(b); err != nil {
			return nil, err
		}
		hopByHops = append(hopByHops, b.hopByHop)
	}
	return hopByHops, nil
}

// sendMember arms this member's own impairment then writes its bytes once,
// or twice at this same position when its config declares duplicate:true.
// Only the first Send pays declared latency: impair.Configure's one-shot
// arming is consumed by that first Send, and this second Send — like
// executeSend's own sendDuplicateCopy — fires immediately afterward.
func (a *Adapter) sendMember(b builtRequest) error {
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

// reorderResults pairs each member with its answer, by Hop-by-Hop ID, and
// returns the slice in DECLARATION order. A member whose answer never
// arrived reports its own timeout, so one unanswered request does not
// obscure the answers the rest of the group did get.
func reorderResults(start time.Time, built []builtRequest, msgs answerSet, raws rawAnswerSet, waitErr error) []*common.StepResult {
	results := make([]*common.StepResult, len(built))
	for _, b := range built {
		results[b.index] = memberResult(start, b, msgs, raws, waitErr)
	}
	return results
}

// memberResult builds one member's own StepResult. A dropped member reports
// the shared dropped result before any map lookup. An answered member
// reports its own answer, stamped duplicated when its own config declared
// it, plus its own declared post-receive delay_ms. An unanswered member
// reports its own timeout/transport-error text, naming its own index, so a
// scenario author can tell which declared sub-step is the one that never
// came back.
//
// latency_ms/jitter_ms are deliberately NOT attributed per member here:
// impair.Transport holds exactly one active Profile at a time, reset by
// every impair.Configure call, and this group's N members share one
// receiveAnswers wait rather than N separate receive calls — there is no
// way to say afterward which member's Receive-side draw actually applied.
// This is an accepted limitation of composing per-member delivery timing
// with a group wait, not an oversight; delay_ms has no such ambiguity,
// since PostReceiveDelay is an adapter-side sleep keyed to this member's own
// config, not a draw on the shared transport.
func memberResult(start time.Time, b builtRequest, msgs answerSet, raws rawAnswerSet, waitErr error) *common.StepResult {
	if impair.DropRequested(b.member.Config) {
		return impair.DroppedResult(start)
	}

	msg, ok := msgs[b.hopByHop]
	if !ok {
		if waitErr != nil {
			return failedStep(start, "reorder member %d: %s", b.index, waitErr)
		}
		return failedStep(start, "reorder member %d: no answer received", b.index)
	}

	result := buildStepResult(start, msg, raws[b.hopByHop])
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
