package sip_test

import (
	"testing"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/protocols/sip"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdapter_TLS_SendRegister(t *testing.T) {
	serverCfg, clientCfg := sip.SelfSignedTLSConfig(t)

	uas := sip.NewTLSMockUAS(t, serverCfg)
	defer uas.Close()

	adapter, err := sip.NewAdapterTLS(":0", clientCfg, 3*time.Second)
	require.NoError(t, err)
	defer adapter.Close()

	result, err := adapter.Execute("send", uas.Addr(), "REGISTER", map[string]string{
		"From":    "<sip:447700900001@ims.local>;tag=tls001",
		"To":      "<sip:447700900001@ims.local>",
		"Contact": "<sip:447700900001@10.0.0.1:5061>",
	}, nil)
	require.NoError(t, err)

	assert.Equal(t, common.StatusPassed, result.Status, "error: %s", result.Error)
	assert.Equal(t, 200, result.Response["status_code"])
}

// TestAdapter_TLS_ViaHeader verifies the Via header carries SIP/2.0/TLS.
// The mock UAS echoes Via back in the response, so we inspect the response header.
func TestAdapter_TLS_ViaHeader(t *testing.T) {
	serverCfg, clientCfg := sip.SelfSignedTLSConfig(t)

	uas := sip.NewTLSMockUAS(t, serverCfg)
	defer uas.Close()

	adapter, err := sip.NewAdapterTLS(":0", clientCfg, 3*time.Second)
	require.NoError(t, err)
	defer adapter.Close()

	result, err := adapter.Execute("send", uas.Addr(), "OPTIONS", map[string]string{
		"From": "<sip:447700900001@ims.local>;tag=viatls",
		"To":   "<sip:ims.local>",
	}, nil)
	require.NoError(t, err)
	require.Equal(t, common.StatusPassed, result.Status)

	headers, ok := result.Response["header"].(map[string]interface{})
	require.True(t, ok, "response headers missing")
	via, _ := headers["Via"].(string)
	assert.Contains(t, via, "SIP/2.0/TLS", "Via header should use TLS transport token")
}

// TestAdapter_TLS_InterfaceCompliance ensures NewAdapterTLS satisfies ProtocolAdapter.
func TestAdapter_TLS_InterfaceCompliance(t *testing.T) {
	_, clientCfg := sip.SelfSignedTLSConfig(t)
	adapter, err := sip.NewAdapterTLS(":0", clientCfg, time.Second)
	require.NoError(t, err)
	defer adapter.Close()

	var _ common.ProtocolAdapter = adapter
}
