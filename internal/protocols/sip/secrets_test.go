package sip

import (
	"strings"
	"sync"
	"testing"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func challengedAdapter(t *testing.T) *Adapter {
	t.Helper()
	mock, a := authFixture(t)
	mock.SetResponder(DigestChallengeResponder(401, "Unauthorized", HeaderWWWAuth, []string{challengeMD5}, func(*SIPMessage) error { return nil }))
	res := runRegister(t, a, mock.Addr(), authFields(), authConfig())
	require.Equal(t, common.StatusPassed, res.Status, res.Error)
	return a
}

func TestAdapterSecrets_ChallengedStepRecordsPasswordAndHeader(t *testing.T) {
	got := challengedAdapter(t).Secrets()
	assert.Contains(t, got, "shared-pw")
	var header bool
	for _, s := range got {
		header = header || (strings.HasPrefix(s, "Digest ") && strings.Contains(s, "response="))
	}
	assert.True(t, header, "the credential header value that went on the wire must be recorded")
}

func TestAdapterSecrets_NoEmptyAndNoDuplicates(t *testing.T) {
	a := challengedAdapter(t)
	a.recordSecret("")
	a.recordSecret("shared-pw")
	seen := map[string]bool{}
	for _, s := range a.Secrets() {
		assert.NotEmpty(t, s)
		assert.False(t, seen[s], "duplicate %q", s)
		seen[s] = true
	}
}

func TestAdapterSecrets_UnchallengedRunRegistersOnlyTheDeclaredPassword(t *testing.T) {
	mock, a := authFixture(t)
	mock.SetResponder(func(req *SIPMessage) []*SIPMessage {
		return []*SIPMessage{BuildResponse(200, "OK", map[string]string{
			"From": req.Headers["From"], "To": req.Headers["To"], "Call-ID": req.Headers["Call-ID"],
			"CSeq": req.Headers["CSeq"], "Via": req.Headers["Via"],
		})}
	})
	res := runRegister(t, a, mock.Addr(), authFields(), authConfig())
	require.Equal(t, common.StatusPassed, res.Status, res.Error)
	assert.Equal(t, []string{"shared-pw"}, a.Secrets(), "only the declared password; nothing is resolved without a challenge")
}

func TestAdapterSecrets_ReturnsACopy(t *testing.T) {
	a := challengedAdapter(t)
	got := a.Secrets()
	got[0] = "tampered"
	assert.NotContains(t, a.Secrets(), "tampered")
}

// Sub-steps of a parallel group share one adapter, so recording and reading
// must be safe from many goroutines. Run under -race.
func TestAdapterSecrets_ConcurrentRecordAndRead(t *testing.T) {
	a := &Adapter{}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				a.recordSecret("pw")
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_ = a.Secrets()
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, []string{"pw"}, a.Secrets())
}
