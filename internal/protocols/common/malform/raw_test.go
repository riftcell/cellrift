package malform_test

import (
	"testing"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/protocols/common/malform"
	"github.com/stretchr/testify/assert"
)

func TestRawPayload(t *testing.T) {
	cases := []struct {
		name   string
		cfg    map[string]string
		want   string
		wantOK bool
	}{
		{
			name:   "absent",
			cfg:    map[string]string{},
			wantOK: false,
		},
		{
			name:   "present",
			cfg:    map[string]string{malform.RawPayloadKey: "\x01\x02"},
			want:   "\x01\x02",
			wantOK: true,
		},
		{
			// An empty payload is not a payload — matches the drop/duplicate
			// boolean flags' own "absent and false mean the same thing" rule.
			name:   "empty is not a payload",
			cfg:    map[string]string{malform.RawPayloadKey: ""},
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := malform.RawPayload(tc.cfg)
			assert.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				assert.Equal(t, tc.want, got)
			}
		})
	}
}

func TestRawResult(t *testing.T) {
	start := time.Now()
	result := malform.RawResult(start, 42)

	assert.Equal(t, common.StatusPassed, result.Status)
	assert.Nil(t, result.RawResponse)
	assert.Equal(t, 42, result.Response[malform.RawBytesSentKey])
}
