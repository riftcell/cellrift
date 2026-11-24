package impair

import (
	"testing"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/stretchr/testify/assert"
)

func TestMarkTimedOut(t *testing.T) {
	t.Run("nil result", func(t *testing.T) {
		assert.Nil(t, MarkTimedOut(nil, true))
	})

	t.Run("true allocates Response", func(t *testing.T) {
		result := &common.StepResult{}
		MarkTimedOut(result, true)
		assert.Equal(t, true, result.Response[TimedOutKey])
	})

	t.Run("false is stamped, not omitted", func(t *testing.T) {
		result := &common.StepResult{}
		MarkTimedOut(result, false)
		v, ok := result.Response[TimedOutKey]
		assert.True(t, ok, "false must be stamped, not left absent")
		assert.Equal(t, false, v)
	})

	t.Run("leaves sibling keys untouched", func(t *testing.T) {
		result := &common.StepResult{
			Response: common.ResponseFields{
				ImpairDelayKey:     int64(300),
				ConnectionStateKey: ConnStateReset,
			},
		}
		MarkTimedOut(result, true)
		assert.Equal(t, true, result.Response[TimedOutKey])
		assert.Equal(t, int64(300), result.Response[ImpairDelayKey])
		assert.Equal(t, ConnStateReset, result.Response[ConnectionStateKey])
	})
}

func TestEnsureTimedOut(t *testing.T) {
	t.Run("nil result", func(t *testing.T) {
		assert.Nil(t, EnsureTimedOut(nil))
	})

	t.Run("stamps false when absent", func(t *testing.T) {
		result := &common.StepResult{}
		EnsureTimedOut(result)
		assert.Equal(t, false, result.Response[TimedOutKey])
	})

	t.Run("does not overwrite existing true", func(t *testing.T) {
		result := &common.StepResult{Response: common.ResponseFields{TimedOutKey: true}}
		EnsureTimedOut(result)
		assert.Equal(t, true, result.Response[TimedOutKey])
	})

	t.Run("does not overwrite existing false", func(t *testing.T) {
		result := &common.StepResult{Response: common.ResponseFields{TimedOutKey: false}}
		EnsureTimedOut(result)
		assert.Equal(t, false, result.Response[TimedOutKey])
	})
}
