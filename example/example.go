// package example contains a self-contained example of a webhook that passes the cert-manager
// DNS conformance tests
package example

import (
	"fmt"
	"log/slog"
	"sync"

	"github.com/cert-manager/cert-manager/pkg/acme/webhook"
	acme "github.com/cert-manager/cert-manager/pkg/acme/webhook/apis/acme/v1alpha1"
	"github.com/miekg/dns"
	"k8s.io/client-go/rest"
)

type exampleSolver struct {
	name       string
	server     *dns.Server
	txtRecords map[string]string
	sync.RWMutex
}

func (e *exampleSolver) Name() string {
	return e.name
}

func (e *exampleSolver) Present(ch *acme.ChallengeRequest) error {
	e.Lock()
	e.txtRecords[ch.ResolvedFQDN] = ch.Key
	e.Unlock()
	return nil
}

func (e *exampleSolver) CleanUp(ch *acme.ChallengeRequest) error {
	e.Lock()
	delete(e.txtRecords, ch.ResolvedFQDN)
	e.Unlock()
	return nil
}

// Initialize starts the in-memory DNS server and only returns once it is
// listening, so callers can query it immediately. It shuts the server down
// when stopCh is closed.
func (e *exampleSolver) Initialize(_ *rest.Config, stopCh <-chan struct{}) error {
	started := make(chan struct{})
	errCh := make(chan error, 1)
	e.server.NotifyStartedFunc = func() { close(started) }

	go func() { errCh <- e.server.ListenAndServe() }()

	select {
	case <-started:
	case err := <-errCh:
		return fmt.Errorf("example DNS server failed to start on %s: %w", e.server.Addr, err)
	}

	go func() {
		select {
		case <-stopCh:
			if err := e.server.Shutdown(); err != nil {
				slog.Error("failed to shut down example DNS server", "error", err)
			}
		case err := <-errCh:
			if err != nil {
				slog.Error("example DNS server stopped", "error", err)
			}
		}
	}()
	return nil
}

func New(port string) webhook.Solver {
	e := &exampleSolver{
		name:       "example",
		txtRecords: make(map[string]string),
	}
	e.server = &dns.Server{
		Addr:    ":" + port,
		Net:     "udp",
		Handler: dns.HandlerFunc(e.handleDNSRequest),
	}
	return e
}
