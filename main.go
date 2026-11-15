// Command webhook is a cert-manager ACME DNS01 webhook solver for Porkbun.
//
// This repository is a clone of github.com/Talinx/cert-manager-webhook-porkbun,
// which builds on Brandon Sprague's github.com/bcspragu/cert-manager-webhook-porkbun
// and cert-manager's github.com/cert-manager/webhook-example. See NOTICE.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	extapi "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	"github.com/cert-manager/cert-manager/pkg/acme/webhook"
	"github.com/cert-manager/cert-manager/pkg/acme/webhook/apis/acme/v1alpha1"
	"github.com/cert-manager/cert-manager/pkg/acme/webhook/cmd"
	"github.com/cert-manager/cert-manager/pkg/issuer/acme/dns/util"

	"github.com/nr3v0/cert-manager-webhook-porkbun/porkbun"
)

const (
	// requestTimeout bounds one Present/CleanUp call (Secret reads plus
	// Porkbun API calls).
	requestTimeout = 60 * time.Second
	// recordTTL is the TTL, in seconds, of challenge TXT records. 600 is the
	// lowest TTL Porkbun accepts; lower values are silently raised.
	recordTTL = "600"
)

func main() {
	groupName := os.Getenv("GROUP_NAME")
	if groupName == "" {
		klog.Fatal("GROUP_NAME must be specified")
	}

	// Registers the Porkbun solver with the webhook serving library, making it
	// available as an API under groupName. Several solvers can share one
	// webhook; Name() disambiguates them.
	cmd.RunWebhookServer(groupName, &porkbunDNSProviderSolver{})
}

// porkbunDNSProviderSolver presents and cleans up ACME DNS01 challenge TXT
// records through the Porkbun API.
type porkbunDNSProviderSolver struct {
	client kubernetes.Interface
}

var _ webhook.Solver = (*porkbunDNSProviderSolver)(nil)

// porkbunDNSProviderConfig is decoded from the Issuer's webhook solver config.
// It only references Secrets; the keys themselves are never put in the config.
type porkbunDNSProviderConfig struct {
	APIKey       corev1.SecretKeySelector `json:"apiKey"`
	SecretAPIKey corev1.SecretKeySelector `json:"secretApiKey"`
}

// Name is the solverName referenced from the ACME Issuer.
func (p *porkbunDNSProviderSolver) Name() string {
	return "porkbun"
}

// Present creates the challenge TXT record. It is idempotent: if a record with
// the same value already exists it does nothing.
func (p *porkbunDNSProviderSolver) Present(ch *v1alpha1.ChallengeRequest) error {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	pbClient, err := p.newPorkbunClient(ctx, ch)
	if err != nil {
		return fmt.Errorf("failed to init Porkbun client: %w", err)
	}

	domain := util.UnFqdn(ch.ResolvedZone)
	subDomain := getSubDomain(domain, ch.ResolvedFQDN)

	records, err := pbClient.RetrieveDNSRecordsByDomainSubdomainType(ctx, domain, subDomain, "TXT")
	if err != nil {
		return fmt.Errorf("failed to get TXT records for %s: %w", ch.ResolvedFQDN, err)
	}

	for _, rec := range records.Records {
		if rec.Content == ch.Key {
			klog.InfoS("TXT record already present", "fqdn", ch.ResolvedFQDN, "zone", domain)
			return nil
		}
	}

	if _, err := pbClient.CreateDNSRecord(ctx, domain, &porkbun.NewDNSRecord{
		Name:    subDomain,
		Type:    "TXT",
		Content: ch.Key,
		TTL:     recordTTL,
	}); err != nil {
		return fmt.Errorf("failed to create TXT record for %s: %w", ch.ResolvedFQDN, err)
	}

	klog.InfoS("TXT record created", "fqdn", ch.ResolvedFQDN, "zone", domain)
	return nil
}

// CleanUp deletes only the TXT record(s) whose value matches ch.Key, so that
// concurrent validations for the same name are not disturbed.
func (p *porkbunDNSProviderSolver) CleanUp(ch *v1alpha1.ChallengeRequest) error {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	pbClient, err := p.newPorkbunClient(ctx, ch)
	if err != nil {
		return fmt.Errorf("failed to init Porkbun client: %w", err)
	}

	domain := util.UnFqdn(ch.ResolvedZone)
	subDomain := getSubDomain(domain, ch.ResolvedFQDN)

	records, err := pbClient.RetrieveDNSRecordsByDomainSubdomainType(ctx, domain, subDomain, "TXT")
	if err != nil {
		return fmt.Errorf("failed to get TXT records for %s: %w", ch.ResolvedFQDN, err)
	}

	var errs []error
	for _, rec := range records.Records {
		if rec.Content != ch.Key {
			continue
		}
		if _, err := pbClient.DeleteDNSRecordByDomainID(ctx, domain, rec.ID); err != nil {
			errs = append(errs, fmt.Errorf("failed to delete TXT record %s (%s): %w", rec.ID, ch.ResolvedFQDN, err))
			continue
		}
		klog.InfoS("TXT record deleted", "fqdn", ch.ResolvedFQDN, "zone", domain, "id", rec.ID)
	}
	return errors.Join(errs...)
}

// Initialize builds the Kubernetes client used to read credential Secrets.
func (p *porkbunDNSProviderSolver) Initialize(kubeClientConfig *rest.Config, _ <-chan struct{}) error {
	cl, err := kubernetes.NewForConfig(kubeClientConfig)
	if err != nil {
		return fmt.Errorf("failed to create Kubernetes client: %w", err)
	}
	p.client = cl
	return nil
}

// getSubDomain returns the record name relative to domain, e.g.
// ("example.com", "_acme-challenge.www.example.com.") -> "_acme-challenge.www".
// It returns "" when fqdn is the zone apex.
func getSubDomain(domain, fqdn string) string {
	name := util.UnFqdn(fqdn)
	if name == domain {
		return ""
	}
	if sub, ok := strings.CutSuffix(name, "."+domain); ok {
		return sub
	}
	return name
}

func (p *porkbunDNSProviderSolver) newPorkbunClient(ctx context.Context, ch *v1alpha1.ChallengeRequest) (*porkbun.Client, error) {
	cfg, err := loadConfig(ch.Config)
	if err != nil {
		return nil, fmt.Errorf("failed to parse webhook config: %w", err)
	}

	// Porkbun has no ambient credentials, so both Secret references are always
	// required (the original code skipped this check when ambient credentials
	// were allowed and then sent empty keys).
	if err := validate(&cfg); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	apiKey, err := p.secret(ctx, cfg.APIKey, ch.ResourceNamespace)
	if err != nil {
		return nil, fmt.Errorf("apiKey: %w", err)
	}

	secretAPIKey, err := p.secret(ctx, cfg.SecretAPIKey, ch.ResourceNamespace)
	if err != nil {
		return nil, fmt.Errorf("secretApiKey: %w", err)
	}

	return porkbun.New(secretAPIKey, apiKey), nil
}

func (p *porkbunDNSProviderSolver) secret(ctx context.Context, ref corev1.SecretKeySelector, namespace string) (string, error) {
	if p.client == nil {
		return "", errors.New("kubernetes client not initialised")
	}

	secret, err := p.client.CoreV1().Secrets(namespace).Get(ctx, ref.Name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to load secret %s/%s: %w", namespace, ref.Name, err)
	}

	data, ok := secret.Data[ref.Key]
	if !ok {
		return "", fmt.Errorf("key %q not found in secret %s/%s", ref.Key, namespace, ref.Name)
	}

	value := strings.TrimSpace(string(data))
	if value == "" {
		return "", fmt.Errorf("key %q in secret %s/%s is empty", ref.Key, namespace, ref.Name)
	}
	return value, nil
}

func validate(cfg *porkbunDNSProviderConfig) error {
	var errs []error
	if cfg.APIKey.Name == "" || cfg.APIKey.Key == "" {
		errs = append(errs, errors.New("apiKey.name and apiKey.key are required"))
	}
	if cfg.SecretAPIKey.Name == "" || cfg.SecretAPIKey.Key == "" {
		errs = append(errs, errors.New("secretApiKey.name and secretApiKey.key are required"))
	}
	return errors.Join(errs...)
}

// loadConfig decodes the solver's JSON config.
func loadConfig(cfgJSON *extapi.JSON) (porkbunDNSProviderConfig, error) {
	cfg := porkbunDNSProviderConfig{}
	if cfgJSON == nil {
		return cfg, nil
	}
	if err := json.Unmarshal(cfgJSON.Raw, &cfg); err != nil {
		return cfg, fmt.Errorf("error decoding solver config: %w", err)
	}
	return cfg, nil
}
