package diameter

import (
	"fmt"
	"net"
	"time"
)

// handshake.go builds the client's Capabilities-Exchange-Request (CER) — the
// RFC 6733 §5.3.1 mandatory AVP set plus the application advertisement that
// determines whether a real HSS finds a common application at all — and
// doHandshake, which drives the CER out and the CEA back in, wired into
// Adapter.Connect.
//
// doHandshake produces three DISTINCT, named error outcomes,
// never one collapsed "handshake failed":
//   - no CEA arrives within the deadline -> a timeout-shaped error naming
//     neither a command code nor a Result-Code, because neither was ever
//     seen;
//   - the first answer is not a CEA at all (RFC 6733 §5.6's
//     Wait-I-CEA: I-Rcv-Non-CEA -> Error -> Closed) -> a protocol-violation
//     error naming the command code actually received;
//   - a CEA arrives but rejects the CER (Result-Code != 2001) -> an error
//     naming the Result-Code and its RFC name (resultCodeName), so a 5010
//     reads as DIAMETER_NO_COMMON_APPLICATION, not a bare number.

// hostIPFromAddr extracts the IP from addr (as returned by a connected
// Transport.LocalAddr()) for use as the CER's Host-IP-Address. It rejects a
// nil or unspecified ("::" / "0.0.0.0") address rather than silently
// returning it: a wildcard Host-IP-Address produces a symptom
// indistinguishable from an Origin-Host misconfiguration against a real
// HSS — better to fail loudly here, at CER-build time, than ship an
// unroutable address to a peer.
func hostIPFromAddr(addr net.Addr) (net.IP, error) {
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return nil, fmt.Errorf("hostIPFromAddr: split %q: %w", addr.String(), err)
	}

	ip := net.ParseIP(host)
	if ip == nil {
		return nil, fmt.Errorf("hostIPFromAddr: %q is not a parseable IP", host)
	}
	if ip.IsUnspecified() {
		return nil, fmt.Errorf("hostIPFromAddr: %q is the unspecified address — call this after Connect, not before", host)
	}
	return ip, nil
}

// vendorSpecificApplicationAVPs renders supportedVendorApps into one
// Vendor-Specific-Application-Id (AVP 260) grouped AVP per entry, each
// wrapping {Vendor-Id, Auth-Application-Id}. Extracted from buildCER because
// the literal would otherwise grow buildCER past the 40-line cap as more
// vendor-scoped interfaces are added (S6a, for instance).
func vendorSpecificApplicationAVPs() []AVP {
	var avps []AVP
	for _, vsa := range supportedVendorApps {
		avps = append(avps, NewGroupedAVP(AVPCodeVendorSpecificApplicationID, []AVP{
			NewUint32AVP(AVPCodeVendorID, vsa.VendorID),
			NewUint32AVP(AVPCodeAuthApplicationID, vsa.AppID),
		}))
	}
	return avps
}

// buildCER constructs a Capabilities-Exchange-Request per RFC 6733 §5.3.1's
// mandatory set (Origin-Host, Origin-Realm, >=1 Host-IP-Address, Vendor-Id,
// Product-Name), plus the application advertisement a real HSS needs to find
// a common application. It returns the encoded bytes and the generated
// Hop-by-Hop ID for answer correlation.
//
// The header Application-Id is always 0, per RFC 6733 §6.11's explicit
// CER/CEA exception — this holds regardless of which applications are
// advertised inside the message body. The P-bit is never set: CER MUST NOT
// be proxied (§5.3).
//
// Credit-Control (RFC 4006, app 4) and Cx are advertised through two
// DIFFERENT AVP shapes and are deliberately NOT built by one shared loop:
// Credit-Control is an RFC 4006 BASE application and MUST appear as a bare
// top-level Auth-Application-Id (AVP 258); Cx is a 3GPP VENDOR-SCOPED
// application and MUST appear only inside Vendor-Specific-Application-Id
// (AVP 260) with Vendor-Id 10415. This function is exactly where the two get
// conflated — sending Cx as a bare 258 is arguably grammar-legal but is
// rejected by a 3GPP-profiled HSS, which matches vendor-scoped applications
// against AVP 260, not the bare Auth-Application-Id list. Both are sent
// unconditionally (not gated on
// which command a scenario happens to send first) so the existing UAR-only
// and CCR-only tests keep working.
func buildCER(originHost, originRealm string, hostIP net.IP) ([]byte, uint32, error) {
	avps := []AVP{
		NewStringAVP(AVPCodeOriginHost, originHost),
		NewStringAVP(AVPCodeOriginRealm, originRealm),
		NewAddressAVP(AVPCodeHostIPAddress, hostIP),
		NewUint32AVP(AVPCodeVendorID, VendorID3GPP),
		NewStringAVP(AVPCodeProductName, "cellrift"),
		// Bare Auth-Application-Id: Credit-Control is an RFC 4006 base
		// application, never vendor-scoped. See the function comment above.
		NewUint32AVP(AVPCodeAuthApplicationID, AppIDCreditControl),
	}
	avps = append(avps, vendorSpecificApplicationAVPs()...)

	return BuildRequest(CmdCapabilitiesExchange, 0, avps)
}

// doHandshake drives the CER out and validates the CEA that answers it,
// transitioning a.peer from Closed to Open on success. Ordering below is
// mechanical, not a preference: the CER's Host-IP-Address must come from the
// CONNECTED socket, so LocalAddr can only be read after Connect's
// transport-level dial has already succeeded — this is why doHandshake is
// called from Connect, never before it.
func (a *Adapter) doHandshake(target string) error {
	if err := a.peer.requireState(peerClosed); err != nil {
		return fmt.Errorf("diameter handshake: %w", err)
	}

	hostIP, err := hostIPFromAddr(a.transport.LocalAddr())
	if err != nil {
		return fmt.Errorf("diameter handshake: %w", err)
	}

	raw, hopByHop, err := buildCER(a.originHost, a.originRealm, hostIP)
	if err != nil {
		return fmt.Errorf("diameter handshake: build CER: %w", err)
	}

	a.peer.setState(peerWaitCEA)
	if err := a.transport.Send(raw, target); err != nil {
		a.peer.setState(peerClosed)
		return fmt.Errorf("diameter handshake: send CER: %w", err)
	}

	// Going through the demux loop (session.receiveAnswer), rather than a
	// bare Receive, means a DWR arriving mid-handshake is answered instead
	// of being mistaken for the CEA.
	a.sess = &session{tr: a.transport, target: target, host: a.originHost, realm: a.originRealm}
	cea, _, err := a.sess.receiveAnswer(hopByHop, time.Now().Add(a.timeout))
	if err != nil {
		a.peer.setState(peerClosed)
		return fmt.Errorf("diameter handshake: no CEA from %s within %s: %w", target, a.timeout, err)
	}

	if err := validateCEA(cea); err != nil {
		a.peer.setState(peerClosed)
		return err
	}

	a.acceptCEA(cea)
	return nil
}

// originRealmOf returns the Origin-Realm msg advertises, or "" if absent.
func originRealmOf(msg *Message) string {
	if avp := msg.FindAVP(AVPCodeOriginRealm); avp != nil {
		return avp.StringVal
	}
	return ""
}

// acceptCEA records everything the accepted CEA told us about the peer —
// its applications (the unsupported-application gate) and its realm (RFC 6733 §6.6's
// Destination-Realm source: "Diameter servers initiating a request message
// use the value of the Origin-Realm AVP from a previous message received
// from the intended target host") — and opens the peer.
func (a *Adapter) acceptCEA(cea *Message) {
	a.peer.setApps(advertisedApplications(cea))
	a.peerRealm = originRealmOf(cea)
	a.peer.setState(peerOpen)
}

// validateCEA checks msg's command code and Result-Code, the two ways a CEA
// can reject a CER (the other two distinct outcomes, beyond the timeout
// doHandshake itself handles):
//
//   - a first response that is not a CEA at all is a protocol violation
//     naming the command code actually received (RFC 6733 §5.6's
//     Wait-I-CEA: I-Rcv-Non-CEA -> Error -> Closed) — this is NOT the same
//     failure as a timeout and must not read like one;
//   - a CEA whose Result-Code is not 2001 names the code and its RFC name
//     via resultCodeName, so a 5010 reads as
//     DIAMETER_NO_COMMON_APPLICATION rather than a bare number. RFC 6733
//     §5.3 notes a peer sending 5010 "SHOULD disconnect the transport
//     connection" — this function never depends on being able to write to
//     the connection again after detecting the rejection.
//
// Extracted from doHandshake to keep it under CLAUDE.md's 40-line cap.
func validateCEA(msg *Message) error {
	if msg.CommandCode != CmdCapabilitiesExchange {
		return fmt.Errorf("diameter handshake: protocol violation: expected CEA (command %d), got command %d", CmdCapabilitiesExchange, msg.CommandCode)
	}
	if rc := msg.ResultCode(); rc != ResultSuccess {
		return fmt.Errorf("diameter handshake: peer rejected CER: Result-Code %d (%s)", rc, resultCodeName(rc))
	}
	return nil
}
