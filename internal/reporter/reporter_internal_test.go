package reporter

import (
	"testing"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/stretchr/testify/assert"
)

// TestImpairDelayMs pins impairDelayMs's extraction and type-handling rules.
// It lives in this internal test file (package reporter, not reporter_test)
// because impairDelayMs is unexported and every other reporter test in this
// package lives in reporter_test.go's external test package.
func TestImpairDelayMs(t *testing.T) {
	tests := []struct {
		name   string
		result *common.StepResult
		wantMs int64
		wantOK bool
	}{
		{
			name:   "int64 value present",
			result: &common.StepResult{Response: common.ResponseFields{impairDelayKey: int64(320)}},
			wantMs: 320,
			wantOK: true,
		},
		{
			name:   "int value present",
			result: &common.StepResult{Response: common.ResponseFields{impairDelayKey: 320}},
			wantMs: 320,
			wantOK: true,
		},
		{
			name:   "nil Response",
			result: &common.StepResult{},
			wantMs: 0,
			wantOK: false,
		},
		{
			name:   "empty Response",
			result: &common.StepResult{Response: common.ResponseFields{}},
			wantMs: 0,
			wantOK: false,
		},
		{
			name:   "Response missing the key",
			result: &common.StepResult{Response: common.ResponseFields{"other_key": int64(5)}},
			wantMs: 0,
			wantOK: false,
		},
		{
			name:   "non-numeric value",
			result: &common.StepResult{Response: common.ResponseFields{impairDelayKey: "320"}},
			wantMs: 0,
			wantOK: false,
		},
		{
			name:   "zero delay",
			result: &common.StepResult{Response: common.ResponseFields{impairDelayKey: int64(0)}},
			wantMs: 0,
			wantOK: false,
		},
		{
			name:   "nil StepResult",
			result: nil,
			wantMs: 0,
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ms, ok := impairDelayMs(tt.result)
			assert.Equal(t, tt.wantMs, ms)
			assert.Equal(t, tt.wantOK, ok)
		})
	}
}
