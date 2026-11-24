package diameter

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPeer_StateMachine(t *testing.T) {
	t.Run("zero value is peerClosed", func(t *testing.T) {
		var p peer
		require.NoError(t, p.requireState(peerClosed))
	})

	t.Run("requireState(peerClosed) succeeds at the zero value", func(t *testing.T) {
		var p peer
		assert.NoError(t, p.requireState(peerClosed))
	})

	t.Run("requireState(peerOpen) at the zero value fails naming both states in words", func(t *testing.T) {
		var p peer
		err := p.requireState(peerOpen)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "Open")
		assert.Contains(t, err.Error(), "Closed")
	})

	t.Run("Open is not reachable by skipping Wait-I-CEA", func(t *testing.T) {
		var p peer
		p.setState(peerWaitCEA)
		err := p.requireState(peerOpen)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "Open")
		assert.Contains(t, err.Error(), "Wait-I-CEA")
	})

	t.Run("after setState(peerOpen) and setApps, supports reflects the confirmed set", func(t *testing.T) {
		var p peer
		p.setState(peerWaitCEA)
		p.setState(peerOpen)
		p.setApps([]uint32{AppIDCx})

		require.NoError(t, p.requireState(peerOpen))
		assert.True(t, p.supports(AppIDCx))
		assert.False(t, p.supports(AppIDS6a))
	})

	t.Run("supports is false before any CEA is processed", func(t *testing.T) {
		var p peer
		assert.False(t, p.supports(AppIDCx))
	})
}

// TestPeer_ConcurrentAccessIsRaceFree exercises setState/requireState/
// supports/setApps concurrently under -race.
func TestPeer_ConcurrentAccessIsRaceFree(t *testing.T) {
	var p peer
	var wg sync.WaitGroup

	const n = 50
	wg.Add(n * 4)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			p.setState(peerWaitCEA)
		}()
		go func() {
			defer wg.Done()
			_ = p.requireState(peerOpen)
		}()
		go func() {
			defer wg.Done()
			p.setApps([]uint32{AppIDCx})
		}()
		go func() {
			defer wg.Done()
			_ = p.supports(AppIDCx)
		}()
	}
	wg.Wait()
}
