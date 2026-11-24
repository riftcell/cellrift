package common_test

import (
	"fmt"
	"io"
	"net"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIsTimeoutErr proves common.IsTimeoutErr classifies a Receive deadline
// expiry — and only that — as a timeout, including through fmt.Errorf
// wrapping and against a real expired net.Conn read deadline, not just a
// synthetic sentinel.
func TestIsTimeoutErr(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		assert.False(t, common.IsTimeoutErr(nil))
	})

	t.Run("os.ErrDeadlineExceeded", func(t *testing.T) {
		assert.True(t, common.IsTimeoutErr(os.ErrDeadlineExceeded))
	})

	t.Run("wrapped os.ErrDeadlineExceeded", func(t *testing.T) {
		err := fmt.Errorf("receive: %w", os.ErrDeadlineExceeded)
		assert.True(t, common.IsTimeoutErr(err))
	})

	t.Run("io.EOF", func(t *testing.T) {
		assert.False(t, common.IsTimeoutErr(io.EOF))
	})

	t.Run("net.ErrClosed", func(t *testing.T) {
		assert.False(t, common.IsTimeoutErr(net.ErrClosed))
	})

	t.Run("syscall.ECONNRESET", func(t *testing.T) {
		assert.False(t, common.IsTimeoutErr(syscall.ECONNRESET))
	})

	t.Run("real expired read deadline", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer ln.Close()
		go func() {
			c, _ := ln.Accept()
			if c != nil {
				defer c.Close()
				select {}
			}
		}()

		conn, err := net.Dial("tcp", ln.Addr().String())
		require.NoError(t, err)
		defer conn.Close()
		require.NoError(t, conn.SetReadDeadline(time.Now().Add(50*time.Millisecond)))
		_, readErr := conn.Read(make([]byte, 1))
		assert.True(t, common.IsTimeoutErr(readErr), "a real expired read deadline must classify as a timeout, got %v", readErr)
	})
}
