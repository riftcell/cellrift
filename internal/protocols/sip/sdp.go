package sip

import (
	"fmt"
	"strconv"
	"strings"
)

// SDPTemplate returns a built-in SDP offer body for the given template name.
//
// Available templates:
//   - "basic_audio_offer" — minimal G.711 offer (PCMA/PCMU + telephone-event).
//     Uses 0.0.0.0 as the connection address and port 49170 as the RTP port.
//     Port 0 means "inactive" per RFC 4566 §5.14 and is rejected by real UAS
//     implementations (Asterisk returns 488); 49170 is the standard example port.
//     Cellrift does not send actual RTP media — the port is a signalling placeholder.
func SDPTemplate(name string) (string, error) {
	switch name {
	case "basic_audio_offer":
		return basicAudioOffer(), nil
	default:
		return "", fmt.Errorf("unknown SDP template %q (available: basic_audio_offer)", name)
	}
}

func basicAudioOffer() string {
	lines := []string{
		"v=0",
		"o=cellrift 0 0 IN IP4 0.0.0.0",
		"s=cellrift",
		"c=IN IP4 0.0.0.0",
		"t=0 0",
		"m=audio 49170 RTP/AVP 8 0 101",
		"a=rtpmap:8 PCMA/8000",
		"a=rtpmap:0 PCMU/8000",
		"a=rtpmap:101 telephone-event/8000",
		"a=fmtp:101 0-15",
		"a=sendrecv",
	}
	return strings.Join(lines, "\r\n") + "\r\n"
}

// ParseSDP extracts key fields from an SDP body for use in capture and assertions.
//
// Returned map keys:
//   - "port"  int    — RTP port from the first audio media line (0 if absent)
//   - "codec" string — encoding name from the first a=rtpmap line (e.g. "PCMA")
func ParseSDP(body []byte) map[string]interface{} {
	result := map[string]interface{}{
		"port":  0,
		"codec": "",
	}
	for _, raw := range strings.Split(string(body), "\n") {
		line := strings.TrimRight(raw, "\r")

		// m=audio PORT PROTO PT1 PT2 …
		if strings.HasPrefix(line, "m=audio ") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				if port, err := strconv.Atoi(parts[1]); err == nil {
					result["port"] = port
				}
			}
		}

		// a=rtpmap:PT CODEC/RATE — first entry wins
		if strings.HasPrefix(line, "a=rtpmap:") {
			if result["codec"] == "" {
				after := strings.TrimPrefix(line, "a=rtpmap:")
				parts := strings.SplitN(after, " ", 2)
				if len(parts) == 2 {
					result["codec"] = strings.SplitN(parts[1], "/", 2)[0]
				}
			}
		}
	}
	return result
}
