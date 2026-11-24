package malform_test

import (
	"testing"

	"github.com/riftcell/cellrift/internal/protocols/common/malform"
	"github.com/stretchr/testify/assert"
)

func TestAVPLengthOverride(t *testing.T) {
	cases := []struct {
		name      string
		cfg       map[string]string
		wantName  string
		wantValue int
		wantOK    bool
	}{
		{
			name:   "absent",
			cfg:    map[string]string{},
			wantOK: false,
		},
		{
			name: "present",
			cfg: map[string]string{
				malform.AVPLengthNameKey:  "Session-Id",
				malform.AVPLengthValueKey: "9999",
			},
			wantName:  "Session-Id",
			wantValue: 9999,
			wantOK:    true,
		},
		{
			name: "non-numeric value",
			cfg: map[string]string{
				malform.AVPLengthNameKey:  "Session-Id",
				malform.AVPLengthValueKey: "not-a-number",
			},
			wantOK: false,
		},
		{
			name: "name without value",
			cfg: map[string]string{
				malform.AVPLengthNameKey: "Session-Id",
			},
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name, value, ok := malform.AVPLengthOverride(tc.cfg)
			assert.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				assert.Equal(t, tc.wantName, name)
				assert.Equal(t, tc.wantValue, value)
			}
		})
	}
}

func TestMessageLengthOverride(t *testing.T) {
	cases := []struct {
		name      string
		cfg       map[string]string
		wantValue int
		wantOK    bool
	}{
		{name: "absent", cfg: map[string]string{}, wantOK: false},
		{name: "zero is declared, not absent", cfg: map[string]string{malform.MsgLengthKey: "0"}, wantValue: 0, wantOK: true},
		{name: "positive", cfg: map[string]string{malform.MsgLengthKey: "9999"}, wantValue: 9999, wantOK: true},
		{name: "non-numeric", cfg: map[string]string{malform.MsgLengthKey: "bad"}, wantOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value, ok := malform.MessageLengthOverride(tc.cfg)
			assert.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				assert.Equal(t, tc.wantValue, value)
			}
		})
	}
}

func TestSIPHeaderOverride(t *testing.T) {
	cases := []struct {
		name   string
		cfg    map[string]string
		want   malform.HeaderSpec
		wantOK bool
	}{
		{
			name:   "absent",
			cfg:    map[string]string{},
			wantOK: false,
		},
		{
			name: "missing op, no value needed",
			cfg: map[string]string{
				malform.SIPHeaderNameKey: "Via",
				malform.SIPHeaderOpKey:   malform.OpMissing,
			},
			want:   malform.HeaderSpec{Name: "Via", Op: malform.OpMissing},
			wantOK: true,
		},
		{
			name: "overwrite with value",
			cfg: map[string]string{
				malform.SIPHeaderNameKey:  "Via",
				malform.SIPHeaderOpKey:    malform.OpOverwrite,
				malform.SIPHeaderValueKey: "garbage",
			},
			want:   malform.HeaderSpec{Name: "Via", Op: malform.OpOverwrite, Value: "garbage"},
			wantOK: true,
		},
		{
			name: "name without op",
			cfg: map[string]string{
				malform.SIPHeaderNameKey: "Via",
			},
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := malform.SIPHeaderOverride(tc.cfg)
			assert.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				assert.Equal(t, tc.want, got)
			}
		})
	}
}
