package impair

import (
	"testing"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
)

// fixedDelayTransport is a hand-written common.Transport plus delayReporter
// stand-in whose LastDelay is a fixed, pre-set value rather than one that
// accrues from real sleeps — RecordDelay's arithmetic can then be tested
// without paying real wall-clock time.
type fixedDelayTransport struct {
	*fakeTransport
	delay time.Duration
}

func (f *fixedDelayTransport) LastDelay() time.Duration { return f.delay }

func TestConfigure_ArmsWrappedTransport(t *testing.T) {
	w := Wrap(&fakeTransport{})

	if err := Configure(w, map[string]string{"latency_ms": "200"}); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	tr := w.(*Transport)
	if !tr.sendArmed || !tr.recvArmed {
		t.Fatalf("Configure did not arm the transport: sendArmed=%v recvArmed=%v", tr.sendArmed, tr.recvArmed)
	}
}

// bareTransport implements only common.Transport, with no
// common.ImpairmentControl — standing in for a transport type this package
// never wraps.
type bareTransport struct {
	*fakeTransport
}

func TestConfigure_NoopOnNonImpairmentCapableTransport(t *testing.T) {
	bare := &bareTransport{fakeTransport: &fakeTransport{}}

	if err := Configure(bare, map[string]string{"latency_ms": "200"}); err != nil {
		t.Fatalf("Configure on non-impairment-capable transport returned error: %v", err)
	}
}

func TestConfigure_PropagatesParseError(t *testing.T) {
	w := Wrap(&fakeTransport{})

	if err := Configure(w, map[string]string{"latency_ms": "oops"}); err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestClear_DisarmsLeftoverProfile(t *testing.T) {
	w := Wrap(&fakeTransport{})

	if err := Configure(w, map[string]string{"latency_ms": "200"}); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	Clear(w)

	start := time.Now()
	if _, _, err := w.Receive(time.Second); err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if elapsed := time.Since(start); elapsed >= 20*time.Millisecond {
		t.Fatalf("Receive after Clear took %s, want < 20ms (disarmed)", elapsed)
	}
}

func TestPostReceiveDelay_SleepsAndReturnsDuration(t *testing.T) {
	start := time.Now()
	d := PostReceiveDelay(map[string]string{"delay_ms": "120"})
	elapsed := time.Since(start)

	if d < 110*time.Millisecond || d > 180*time.Millisecond {
		t.Fatalf("returned duration %s, want ~120ms", d)
	}
	if elapsed < 110*time.Millisecond {
		t.Fatalf("elapsed %s, want >= ~120ms (actually slept)", elapsed)
	}
}

func TestPostReceiveDelay_NoopCases(t *testing.T) {
	cases := []struct {
		name string
		cfg  map[string]string
	}{
		{"empty cfg", map[string]string{}},
		{"nil cfg", nil},
		{"zero delay", map[string]string{"delay_ms": "0"}},
		{"junk delay", map[string]string{"delay_ms": "junk"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()
			d := PostReceiveDelay(tc.cfg)
			elapsed := time.Since(start)

			if d != 0 {
				t.Fatalf("returned %s, want 0", d)
			}
			if elapsed >= 20*time.Millisecond {
				t.Fatalf("elapsed %s, want < 20ms (no sleep)", elapsed)
			}
		})
	}
}

func TestRecordDelay_SumsTransportAndPostReceive(t *testing.T) {
	tr := &fixedDelayTransport{fakeTransport: &fakeTransport{}, delay: 200 * time.Millisecond}
	result := &common.StepResult{}

	got := RecordDelay(tr, result, 120*time.Millisecond)

	if got != result {
		t.Fatalf("RecordDelay did not return the same *StepResult")
	}
	v, ok := result.Response[ImpairDelayKey]
	if !ok {
		t.Fatalf("Response missing %s key", ImpairDelayKey)
	}
	ms, ok := v.(int64)
	if !ok {
		t.Fatalf("Response[%s] is %T, want int64", ImpairDelayKey, v)
	}
	if ms != 320 {
		t.Fatalf("Response[%s] = %d, want 320", ImpairDelayKey, ms)
	}
}

func TestRecordDelay_AllocatesResponseWhenNil(t *testing.T) {
	tr := &fixedDelayTransport{fakeTransport: &fakeTransport{}, delay: 50 * time.Millisecond}
	result := &common.StepResult{Response: nil}

	RecordDelay(tr, result, 0)

	if result.Response == nil {
		t.Fatalf("Response still nil after an impaired RecordDelay")
	}
	if _, ok := result.Response[ImpairDelayKey]; !ok {
		t.Fatalf("Response missing %s key", ImpairDelayKey)
	}
}

func TestRecordDelay_LeavesExistingResponseKeysUntouched(t *testing.T) {
	tr := &fixedDelayTransport{fakeTransport: &fakeTransport{}, delay: 50 * time.Millisecond}
	result := &common.StepResult{Response: common.ResponseFields{"status_code": "200"}}

	RecordDelay(tr, result, 0)

	if result.Response["status_code"] != "200" {
		t.Fatalf("existing key was disturbed: %+v", result.Response)
	}
	if _, ok := result.Response[ImpairDelayKey]; !ok {
		t.Fatalf("Response missing %s key", ImpairDelayKey)
	}
}

func TestRecordDelay_LeavesUnimpairedResultAlone(t *testing.T) {
	tr := &fixedDelayTransport{fakeTransport: &fakeTransport{}, delay: 0}
	result := &common.StepResult{}

	RecordDelay(tr, result, 0)

	if result.Response != nil {
		t.Fatalf("Response = %+v, want nil for an unimpaired step", result.Response)
	}
}

func TestRecordDelay_NoopOnNonDelayReporterTransport(t *testing.T) {
	bare := &bareTransport{fakeTransport: &fakeTransport{}}
	result := &common.StepResult{}

	got := RecordDelay(bare, result, 0)

	if got != result {
		t.Fatalf("RecordDelay did not return the same *StepResult")
	}
	if result.Response != nil {
		t.Fatalf("Response = %+v, want nil", result.Response)
	}
}

func TestRecordDelay_NilResultDoesNotPanic(t *testing.T) {
	tr := &fixedDelayTransport{fakeTransport: &fakeTransport{}, delay: 100 * time.Millisecond}

	if got := RecordDelay(tr, nil, 0); got != nil {
		t.Fatalf("RecordDelay(tr, nil, d) = %v, want nil", got)
	}
}

func TestDropRequested(t *testing.T) {
	cases := []struct {
		name string
		cfg  map[string]string
		want bool
	}{
		{"drop true", map[string]string{"drop": "true"}, true},
		{"nil cfg", nil, false},
		{"non-literal value", map[string]string{"drop": "1"}, false},
		{"response key mistaken for config key", map[string]string{"dropped": "true"}, false},
		{"absent", map[string]string{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DropRequested(tc.cfg); got != tc.want {
				t.Fatalf("DropRequested(%v) = %v, want %v", tc.cfg, got, tc.want)
			}
		})
	}
}

func TestDuplicateRequested(t *testing.T) {
	cases := []struct {
		name string
		cfg  map[string]string
		want bool
	}{
		{"duplicate true", map[string]string{"duplicate": "true"}, true},
		{"nil cfg", nil, false},
		{"non-literal value", map[string]string{"duplicate": "1"}, false},
		{"response key mistaken for config key", map[string]string{"duplicated": "true"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DuplicateRequested(tc.cfg); got != tc.want {
				t.Fatalf("DuplicateRequested(%v) = %v, want %v", tc.cfg, got, tc.want)
			}
		})
	}
}

func TestProfileFromConfig_IgnoresDropAndDuplicate(t *testing.T) {
	p, err := ProfileFromConfig(map[string]string{"drop": "true", "duplicate": "true"})
	if err != nil {
		t.Fatalf("ProfileFromConfig: %v", err)
	}
	if p != (Profile{}) {
		t.Fatalf("ProfileFromConfig(drop/duplicate) = %+v, want zero Profile", p)
	}
}

func TestDroppedResult(t *testing.T) {
	start := time.Now()
	result := DroppedResult(start)

	if result.Status != common.StatusPassed {
		t.Fatalf("Status = %v, want StatusPassed", result.Status)
	}
	if result.DurationMs < 0 {
		t.Fatalf("DurationMs = %d, want >= 0", result.DurationMs)
	}
	v, ok := result.Response[DroppedKey]
	if !ok {
		t.Fatalf("Response missing %s key", DroppedKey)
	}
	if v != true {
		t.Fatalf("Response[%s] = %v, want true", DroppedKey, v)
	}
}

func TestMarkDuplicated_AllocatesAndPreservesExistingKeys(t *testing.T) {
	result := &common.StepResult{Response: common.ResponseFields{ImpairDelayKey: int64(50)}}

	got := MarkDuplicated(result)

	if got != result {
		t.Fatalf("MarkDuplicated did not return the same *StepResult")
	}
	if result.Response[DuplicatedKey] != true {
		t.Fatalf("Response[%s] = %v, want true", DuplicatedKey, result.Response[DuplicatedKey])
	}
	if result.Response[ImpairDelayKey] != int64(50) {
		t.Fatalf("existing key %s was disturbed: %+v", ImpairDelayKey, result.Response)
	}
}

func TestMarkDuplicated_AllocatesResponseWhenNil(t *testing.T) {
	result := &common.StepResult{Response: nil}

	MarkDuplicated(result)

	if result.Response == nil {
		t.Fatalf("Response still nil after MarkDuplicated")
	}
	if result.Response[DuplicatedKey] != true {
		t.Fatalf("Response[%s] = %v, want true", DuplicatedKey, result.Response[DuplicatedKey])
	}
}

func TestMarkDuplicated_NilResultDoesNotPanic(t *testing.T) {
	if got := MarkDuplicated(nil); got != nil {
		t.Fatalf("MarkDuplicated(nil) = %v, want nil", got)
	}
}
