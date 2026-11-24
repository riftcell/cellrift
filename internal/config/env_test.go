package config

import (
	"testing"
	"time"
)

func TestSIPPollIntervalMs_Default(t *testing.T) {
	t.Setenv("CELLRIFT_SIP_POLL_INTERVAL_MS", "")
	if got := SIPPollIntervalMs(); got != 50*time.Millisecond {
		t.Errorf("default = %v, want 50ms", got)
	}
}

func TestSIPPollIntervalMs_Override(t *testing.T) {
	t.Setenv("CELLRIFT_SIP_POLL_INTERVAL_MS", "100")
	if got := SIPPollIntervalMs(); got != 100*time.Millisecond {
		t.Errorf("override = %v, want 100ms", got)
	}
}

func TestSIPMaxProvisional_Default(t *testing.T) {
	t.Setenv("CELLRIFT_SIP_MAX_PROVISIONAL", "")
	if got := SIPMaxProvisional(); got != 50 {
		t.Errorf("default = %d, want 50", got)
	}
}

func TestSIPMaxProvisional_Override(t *testing.T) {
	t.Setenv("CELLRIFT_SIP_MAX_PROVISIONAL", "10")
	if got := SIPMaxProvisional(); got != 10 {
		t.Errorf("override = %d, want 10", got)
	}
}

func TestSIPDuplicateDrainMs_Default(t *testing.T) {
	t.Setenv("CELLRIFT_SIP_DUPLICATE_DRAIN_MS", "")
	if got := SIPDuplicateDrainMs(); got != 100*time.Millisecond {
		t.Errorf("default = %v, want 100ms", got)
	}
}

func TestSIPDuplicateDrainMs_Override(t *testing.T) {
	t.Setenv("CELLRIFT_SIP_DUPLICATE_DRAIN_MS", "250")
	if got := SIPDuplicateDrainMs(); got != 250*time.Millisecond {
		t.Errorf("override = %v, want 250ms", got)
	}
}

func TestSIPDuplicateDrainMs_InvalidFallsBack(t *testing.T) {
	t.Setenv("CELLRIFT_SIP_DUPLICATE_DRAIN_MS", "not-a-number")
	if got := SIPDuplicateDrainMs(); got != 100*time.Millisecond {
		t.Errorf("invalid override = %v, want default 100ms", got)
	}
}

func TestSIPDuplicateDrainMs_NonPositiveFallsBack(t *testing.T) {
	t.Setenv("CELLRIFT_SIP_DUPLICATE_DRAIN_MS", "0")
	if got := SIPDuplicateDrainMs(); got != 100*time.Millisecond {
		t.Errorf("zero override = %v, want default 100ms", got)
	}
	t.Setenv("CELLRIFT_SIP_DUPLICATE_DRAIN_MS", "-5")
	if got := SIPDuplicateDrainMs(); got != 100*time.Millisecond {
		t.Errorf("negative override = %v, want default 100ms", got)
	}
}

func TestTCPConnectTimeoutMs_Default(t *testing.T) {
	t.Setenv("CELLRIFT_TCP_CONNECT_TIMEOUT_MS", "")
	if got := TCPConnectTimeoutMs(); got != 5000*time.Millisecond {
		t.Errorf("default = %v, want 5000ms", got)
	}
}

func TestTCPConnectTimeoutMs_Override(t *testing.T) {
	t.Setenv("CELLRIFT_TCP_CONNECT_TIMEOUT_MS", "2000")
	if got := TCPConnectTimeoutMs(); got != 2*time.Second {
		t.Errorf("override = %v, want 2s", got)
	}
}

func TestEnvDuration_InvalidFallsBack(t *testing.T) {
	t.Setenv("CELLRIFT_SIP_POLL_INTERVAL_MS", "not-a-number")
	if got := SIPPollIntervalMs(); got != 50*time.Millisecond {
		t.Errorf("invalid override = %v, want default 50ms", got)
	}
}

func TestEnvInt_ZeroFallsBack(t *testing.T) {
	t.Setenv("CELLRIFT_SIP_MAX_PROVISIONAL", "0")
	if got := SIPMaxProvisional(); got != 50 {
		t.Errorf("zero override = %d, want default 50", got)
	}
}

func TestEnvInt_NegativeFallsBack(t *testing.T) {
	t.Setenv("CELLRIFT_SIP_MAX_PROVISIONAL", "-5")
	if got := SIPMaxProvisional(); got != 50 {
		t.Errorf("negative override = %d, want default 50", got)
	}
}

func TestSCTPConnectTimeoutMs(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want time.Duration
	}{
		{"default when unset", "", 5000 * time.Millisecond},
		{"override", "250", 250 * time.Millisecond},
		{"non-numeric falls back", "abc", 5000 * time.Millisecond},
		{"zero falls back", "0", 5000 * time.Millisecond},
		{"negative falls back", "-1", 5000 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CELLRIFT_SCTP_CONNECT_TIMEOUT_MS", tt.env)
			if got := SCTPConnectTimeoutMs(); got != tt.want {
				t.Errorf("SCTPConnectTimeoutMs() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSCTPTimeoutIndependentOfTCP(t *testing.T) {
	t.Setenv("CELLRIFT_SCTP_CONNECT_TIMEOUT_MS", "999")
	t.Setenv("CELLRIFT_TCP_CONNECT_TIMEOUT_MS", "")
	if got := TCPConnectTimeoutMs(); got != 5000*time.Millisecond {
		t.Errorf("setting SCTP timeout changed TCPConnectTimeoutMs() = %v, want 5000ms", got)
	}

	t.Setenv("CELLRIFT_SCTP_CONNECT_TIMEOUT_MS", "")
	t.Setenv("CELLRIFT_TCP_CONNECT_TIMEOUT_MS", "999")
	if got := SCTPConnectTimeoutMs(); got != 5000*time.Millisecond {
		t.Errorf("setting TCP timeout changed SCTPConnectTimeoutMs() = %v, want 5000ms", got)
	}
}

func TestSIPPassword_Unset(t *testing.T) {
	t.Setenv("CELLRIFT_SIP_PASSWORD", "")
	if got := SIPPassword(); got != "" {
		t.Errorf("unset = %q, want empty", got)
	}
}

func TestSIPPassword_Set(t *testing.T) {
	t.Setenv("CELLRIFT_SIP_PASSWORD", "ambient")
	if got := SIPPassword(); got != "ambient" {
		t.Errorf("set = %q, want ambient", got)
	}
}
