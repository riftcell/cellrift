package common

// SecretSource is implemented by an adapter that resolves credentials during a
// run. The runner collects what each adapter resolved after the steps have run
// and hands it to the reporters, so a password that was never resolved is never
// registered and a scenario that does not authenticate is unaffected.
//
// It is an optional interface, type-asserted rather than added to
// ProtocolAdapter: Diameter resolves no credentials today and widening the
// interface would force an empty method on it.
type SecretSource interface {
	// Secrets returns every credential-bearing string this adapter produced,
	// deduplicated. The caller must treat them as write-only: they exist to be
	// removed from output, never to be logged, compared or displayed.
	Secrets() []string
}
