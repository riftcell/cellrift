package common

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

// BuildTLSConfig constructs a *tls.Config from cert file paths.
//
// certFile and keyFile form a PEM client cert/key pair used for mutual TLS.
// caFile is a PEM CA certificate used to verify the server certificate.
// Any path may be empty to skip loading that item.
//
// Server verification is ON by default: against caFile when given, otherwise
// against the system root pool (tls.Config's own behaviour when RootCAs is
// nil). It is disabled ONLY when insecureSkipVerify is true — an explicit,
// per-scenario opt-in for lab gear with self-signed certificates. Before
// this, leaving all three paths empty silently set InsecureSkipVerify, so
// the most natural configuration (transport: tls, nothing else) produced an
// unauthenticated, MITM-able connection with no warning anywhere but a
// source comment.
func BuildTLSConfig(certFile, keyFile, caFile string, insecureSkipVerify bool) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}

	if certFile != "" && keyFile != "" {
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("load cert/key: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}

	if caFile != "" {
		raw, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("read CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(raw) {
			return nil, fmt.Errorf("CA file %q: no valid PEM certificates", caFile)
		}
		cfg.RootCAs = pool
	}

	if insecureSkipVerify {
		cfg.InsecureSkipVerify = true //nolint:gosec // explicit per-scenario opt-in, surfaced as a run warning
	}

	return cfg, nil
}
