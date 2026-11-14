package main

import (
	"os"
	"testing"

	acmetest "github.com/cert-manager/cert-manager/test/acme"
)

// TestRunsSuite runs the cert-manager DNS01 conformance suite against the real
// Porkbun API. It needs a zone you control and credentials, so it is skipped
// unless TEST_ZONE_NAME is set (e.g. TEST_ZONE_NAME=example.com.).
//
// Copy testdata/porkbun/*.sample to the same names without the .sample suffix
// and fill in your credentials before running.
func TestRunsSuite(t *testing.T) {
	zone := os.Getenv("TEST_ZONE_NAME")
	if zone == "" {
		t.Skip("TEST_ZONE_NAME not set; skipping Porkbun conformance suite")
	}

	fixture := acmetest.NewFixture(&porkbunDNSProviderSolver{},
		acmetest.SetResolvedZone(zone),
		acmetest.SetAllowAmbientCredentials(false),
		acmetest.SetManifestPath("testdata/porkbun"),
	)
	fixture.RunConformance(t)
}
