package diameter

import (
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildCER(t *testing.T) {
	hostIP := net.ParseIP("192.0.2.10")
	raw, hopByHop, err := buildCER("cellrift.test.example.com", "test.example.com", hostIP)
	require.NoError(t, err)
	assert.NotZero(t, hopByHop)

	msg, err := ParseMessage(raw)
	require.NoError(t, err)

	assert.Equal(t, CmdCapabilitiesExchange, msg.CommandCode)
	assert.True(t, msg.IsRequest(), "CER must have the R-bit set")
	assert.Zero(t, msg.Flags&FlagProxiable, "CER MUST NOT be proxied — P-bit must be clear (RFC 6733 §5.3)")
	assert.Equal(t, uint32(0), msg.AppID, "CER/CEA always carry header Application-Id 0 — RFC 6733 §6.11's explicit exception")

	originHost := msg.FindAVP(AVPCodeOriginHost)
	require.NotNil(t, originHost, "Origin-Host is mandatory — RFC 6733 §5.3.1")
	assert.Equal(t, "cellrift.test.example.com", originHost.StringVal)

	originRealm := msg.FindAVP(AVPCodeOriginRealm)
	require.NotNil(t, originRealm, "Origin-Realm is mandatory — RFC 6733 §5.3.1")
	assert.Equal(t, "test.example.com", originRealm.StringVal)

	assert.NotNil(t, msg.FindAVP(AVPCodeHostIPAddress), "Host-IP-Address is mandatory (at least one) — RFC 6733 §5.3.1")

	vendorID := msg.FindAVP(AVPCodeVendorID)
	require.NotNil(t, vendorID, "Vendor-Id is mandatory — RFC 6733 §5.3.1")
	assert.Equal(t, VendorID3GPP, vendorID.Uint32Val)

	assert.NotNil(t, msg.FindAVP(AVPCodeProductName), "Product-Name is mandatory — RFC 6733 §5.3.1")
}

// TestBuildCER_HostIPAddressIsNotWildcard asserts more than mere presence:
// decodes AVP 257 and reconstructs the net.IP, then checks it is not the
// unspecified address. Asserting presence alone would pass against a
// wildcard Host-IP-Address today and still be rejected by a real HSS — this
// stronger assertion is what makes the Host-IP-Address check meaningful
// rather than a rubber-stamp.
func TestBuildCER_HostIPAddressIsNotWildcard(t *testing.T) {
	hostIP := net.ParseIP("203.0.113.5")
	raw, _, err := buildCER("cellrift.test.example.com", "test.example.com", hostIP)
	require.NoError(t, err)

	msg, err := ParseMessage(raw)
	require.NoError(t, err)

	avp := msg.FindAVP(AVPCodeHostIPAddress)
	require.NotNil(t, avp)
	require.GreaterOrEqual(t, len(avp.Data), 2, "Address-format AVP must carry at least the 2-octet family")

	decoded := decodeAddressAVPData(t, avp.Data)
	assert.False(t, decoded.IsUnspecified(), "Host-IP-Address must not be the unspecified address")
	assert.NotEqual(t, "::", decoded.String())
	assert.NotEqual(t, "0.0.0.0", decoded.String())
	assert.Equal(t, hostIP.String(), decoded.String())
}

// decodeAddressAVPData reconstructs a net.IP from the raw Data of an
// Address-format AVP (RFC 6733 §4.3.1: 2-octet IANA family + address bytes),
// mirroring what a real peer decoder does — not just checking presence.
func decodeAddressAVPData(t *testing.T, data []byte) net.IP {
	t.Helper()
	require.GreaterOrEqual(t, len(data), 2)
	addrBytes := data[2:]
	ip := net.IP(addrBytes)
	require.NotNil(t, ip)
	return ip
}

// TestBuildCER_VendorSpecificApplication pins the Credit-Control / Cx / S6a
// distinction — a known interop failure when conflated — with a FOUR-PART
// assertion:
//
//	(a) TWO AVP 260s exist, one wrapping {Vendor-Id 10415, Auth-Application-Id
//	    Cx} and one wrapping {Vendor-Id 10415, Auth-Application-Id S6a} —
//	    checked by set membership, since supportedVendorApps is a map and Go
//	    randomises its iteration order;
//	(b) Credit-Control (app 4) appears as a BARE top-level AVP 258;
//	(c) NO AVP 260 anywhere contains a 258 whose value is 4.
//
// (c) is the part a naive test omits, and it is the one that catches a
// builder that wraps everything into Vendor-Specific-Application-Id.
func TestBuildCER_VendorSpecificApplication(t *testing.T) {
	hostIP := net.ParseIP("192.0.2.10")
	raw, _, err := buildCER("cellrift.test.example.com", "test.example.com", hostIP)
	require.NoError(t, err)

	msg, err := ParseMessage(raw)
	require.NoError(t, err)

	wrappedAppIDs := map[uint32]bool{}
	var creditControlWrapped bool
	for i := range msg.AVPs {
		a := &msg.AVPs[i]
		if a.Code != AVPCodeVendorSpecificApplicationID {
			continue
		}
		require.NoError(t, DecodeGroupedAVP(a))

		vendorID := FindAVP(a.GroupedAVPs, AVPCodeVendorID)
		authAppID := FindAVP(a.GroupedAVPs, AVPCodeAuthApplicationID)
		require.NotNil(t, vendorID, "AVP 260 must wrap a Vendor-Id sub-AVP")
		require.NotNil(t, authAppID, "AVP 260 must wrap an Auth-Application-Id sub-AVP")

		// (a) both Cx and S6a are wrapped with Vendor-Id 10415.
		if authAppID.Uint32Val == AppIDCx || authAppID.Uint32Val == AppIDS6a {
			assert.Equal(t, VendorID3GPP, vendorID.Uint32Val, "Cx/S6a must be wrapped with Vendor-Id 10415")
		}
		wrappedAppIDs[authAppID.Uint32Val] = true

		// (c) no AVP 260 anywhere contains a 258 whose value is Credit-Control (4).
		if authAppID.Uint32Val == AppIDCreditControl {
			creditControlWrapped = true
		}
	}
	assert.True(t, wrappedAppIDs[AppIDCx], "expected a Vendor-Specific-Application-Id AVP wrapping Cx (16777216)")
	assert.True(t, wrappedAppIDs[AppIDS6a], "expected a Vendor-Specific-Application-Id AVP wrapping S6a (16777251)")
	assert.False(t, creditControlWrapped, "Credit-Control (app 4) must NEVER appear inside a Vendor-Specific-Application-Id — it is a bare base application")

	// (b) a top-level AVP 258 = 4 exists OUTSIDE any 260.
	var bareCreditControl bool
	for i := range msg.AVPs {
		a := &msg.AVPs[i]
		if a.Code == AVPCodeAuthApplicationID && a.Uint32Val == AppIDCreditControl {
			bareCreditControl = true
		}
	}
	assert.True(t, bareCreditControl, "Credit-Control (app 4) must appear as a bare top-level Auth-Application-Id")
}

func TestHostIPFromAddr_RejectsUnspecified(t *testing.T) {
	addr := &net.TCPAddr{IP: net.IPv4zero, Port: 3868}
	_, err := hostIPFromAddr(addr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unspecified")
}

func TestHostIPFromAddr_RejectsUnspecifiedIPv6(t *testing.T) {
	addr := &net.TCPAddr{IP: net.IPv6unspecified, Port: 3868}
	_, err := hostIPFromAddr(addr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unspecified")
}

func TestHostIPFromAddr_ExtractsIPv4(t *testing.T) {
	addr := &net.TCPAddr{IP: net.ParseIP("198.51.100.7"), Port: 3868}
	ip, err := hostIPFromAddr(addr)
	require.NoError(t, err)
	assert.Equal(t, "198.51.100.7", ip.String())
}

func TestHostIPFromAddr_ExtractsIPv6(t *testing.T) {
	addr := &net.TCPAddr{IP: net.ParseIP("2001:db8::1"), Port: 3868}
	ip, err := hostIPFromAddr(addr)
	require.NoError(t, err)
	assert.Equal(t, "2001:db8::1", ip.String())
}

// A real SCTP association's local address is the CER's Host-IP-Address source.
// SCTP is natively multi-homed: an association dialled without an explicit
// single local IP reports every local interface slash-joined, e.g.
// "127.0.0.1/192.168.0.102/172.17.0.1:38601", which neither net.SplitHostPort
// nor net.ParseIP accepts — so the CER would fail to build on any host with
// more than one interface. This test fails the moment the dial stops binding
// one explicit address.
func TestHostIPFromAddr_AcceptsSCTPLocalAddr(t *testing.T) {
	server, _, err := common.NewSCTPOrFallbackTransport("127.0.0.1:0")
	require.NoError(t, err)
	if _, ok := server.(*common.SCTPTransport); !ok {
		server.Close()
		t.Skip("kernel SCTP unavailable on this machine — the multi-homed local address this guards against can only arise on a real SCTP association")
	}
	defer server.Close()

	client, _, err := common.NewSCTPOrFallbackTransport("127.0.0.1:0")
	require.NoError(t, err)
	defer client.Close()

	connector, ok := client.(common.Connector)
	require.True(t, ok)
	require.NoError(t, connector.Connect(server.LocalAddr().String()))

	local := client.LocalAddr().String()
	require.NotContains(t, local, "/", "SCTP LocalAddr enumerated multiple interfaces — the dial is no longer binding one explicit local address")

	ip, err := hostIPFromAddr(client.LocalAddr())
	require.NoError(t, err)
	require.NotNil(t, ip)
	require.False(t, ip.IsUnspecified())
}

// sctpAddrStub is a tiny net.Addr stand-in that reports the exact multi-homed
// address shape a real, unbound SCTP association produces.
type sctpAddrStub struct{ addr string }

func (s sctpAddrStub) Network() string { return "sctp" }
func (s sctpAddrStub) String() string  { return s.addr }

// TestHostIPFromAddr_RejectsMultiHomedAddr pins the exact address shape the
// SCTP guard above protects against, so that guard cannot quietly become a
// tautology on a single-interface machine.
func TestHostIPFromAddr_RejectsMultiHomedAddr(t *testing.T) {
	addr := sctpAddrStub{addr: "127.0.0.1/192.168.0.102/172.17.0.1:38601"}
	_, err := hostIPFromAddr(addr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hostIPFromAddr")
}

// TestAdapter_Handshake covers the handshake's four mutually distinct outcomes: a
// 2001 CEA opens the peer and records its advertised applications; a 5010
// CEA produces an error naming both "5010"
// and "DIAMETER_NO_COMMON_APPLICATION"; no CEA at all within the deadline
// produces a distinct timeout error that names no result code; and a first
// answer that is not a CEA produces a distinct protocol-violation error
// naming the command code it got. A single collapsed "handshake failed" for
// all four would pass a weaker test and defeat the purpose, so every
// subtest below also asserts its error text differs from the others'.
func TestAdapter_Handshake(t *testing.T) {
	var errs []string

	t.Run("success", func(t *testing.T) {
		hss := NewMockHSS(t) // supports Cx + Credit-Control by default
		defer hss.Close()

		tr, err := common.NewTCPTransport(":0")
		require.NoError(t, err)
		adapter := NewAdapterWithTransport(tr, 2*time.Second)
		defer adapter.Close()

		require.NoError(t, adapter.Connect(hss.Addr(), nil))

		assert.True(t, adapter.peer.supports(AppIDCx), "peer must record Cx from the CEA's advertisement")
		assert.True(t, adapter.peer.supports(AppIDCreditControl), "peer must record Credit-Control from the CEA's advertisement")
		assert.NoError(t, adapter.peer.requireState(peerOpen), "peer must be Open after a 2001 CEA")
	})

	t.Run("no common application", func(t *testing.T) {
		// disjointAppID is neither Cx, S6a, nor Credit-Control — the client's
		// real CER now advertises all three (S6a joined once apps.go's
		// supportedVendorApps gained it), so the disjoint set here must name
		// an application the client never advertises at all.
		const disjointAppID uint32 = 99999
		hss := NewMockHSSWithApps(t, disjointAppID)
		defer hss.Close()

		tr, err := common.NewTCPTransport(":0")
		require.NoError(t, err)
		adapter := NewAdapterWithTransport(tr, 2*time.Second)
		defer adapter.Close()

		err = adapter.Connect(hss.Addr(), nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "5010")
		assert.Contains(t, err.Error(), "DIAMETER_NO_COMMON_APPLICATION")
		assert.Error(t, adapter.peer.requireState(peerOpen), "peer must NOT be Open after a 5010 CEA")
		errs = append(errs, err.Error())
	})

	t.Run("timeout", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer ln.Close()

		go func() {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			defer conn.Close()
			buf := make([]byte, 4096)
			for {
				// Read (and discard) the CER, then every subsequent read —
				// but never answer. The peer closing the client's transport
				// at test teardown is what eventually unblocks this loop.
				if _, err := conn.Read(buf); err != nil {
					return
				}
			}
		}()

		tr, err := common.NewTCPTransport(":0")
		require.NoError(t, err)
		adapter := NewAdapterWithTransport(tr, 100*time.Millisecond)
		defer adapter.Close()

		err = adapter.Connect(ln.Addr().String(), nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no CEA")
		assert.NotContains(t, err.Error(), "Result-Code", "a timeout must not invent a result code")
		errs = append(errs, err.Error())
	})

	t.Run("non-CEA response", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer ln.Close()

		go func() {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			defer conn.Close()
			buf := make([]byte, 4096)
			n, err := conn.Read(buf)
			if err != nil {
				return
			}
			req, err := ParseMessage(buf[:n])
			if err != nil {
				return
			}
			// Answer the CER with a DWA instead of a CEA — RFC 6733 §5.6's
			// Wait-I-CEA: I-Rcv-Non-CEA case.
			avps := []AVP{
				NewUint32AVP(AVPCodeResultCode, ResultSuccess),
				NewStringAVP(AVPCodeOriginHost, "rogue.example.com"),
				NewStringAVP(AVPCodeOriginRealm, "example.com"),
			}
			raw, err := BuildAnswer(CmdDeviceWatchdog, 0, avps)
			if err != nil {
				return
			}
			raw = copyAnswerIdentifiers(raw, req)
			_, _ = conn.Write(raw)
		}()

		tr, err := common.NewTCPTransport(":0")
		require.NoError(t, err)
		adapter := NewAdapterWithTransport(tr, 2*time.Second)
		defer adapter.Close()

		err = adapter.Connect(ln.Addr().String(), nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "protocol violation")
		assert.Contains(t, err.Error(), fmt.Sprint(CmdDeviceWatchdog))
		errs = append(errs, err.Error())
	})

	require.Len(t, errs, 3, "expected one error string from each failing subtest")
	assert.NotEqual(t, errs[0], errs[1], "5010 and timeout errors must read differently")
	assert.NotEqual(t, errs[0], errs[2], "5010 and non-CEA errors must read differently")
	assert.NotEqual(t, errs[1], errs[2], "timeout and non-CEA errors must read differently")
}
