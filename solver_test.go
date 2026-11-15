package main

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	extapi "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestGetSubDomain(t *testing.T) {
	tests := []struct {
		domain, fqdn, want string
	}{
		{"example.com", "_acme-challenge.example.com.", "_acme-challenge"},
		{"example.com", "_acme-challenge.www.example.com.", "_acme-challenge.www"},
		{"example.com", "_acme-challenge.example.com", "_acme-challenge"},
		{"example.com", "example.com.", ""},
		// The zone name appears twice; only the suffix may be stripped.
		{"example.com", "_acme-challenge.example.com.example.com.", "_acme-challenge.example.com"},
		// Not inside the zone: returned unchanged (minus trailing dot).
		{"example.com", "_acme-challenge.other.org.", "_acme-challenge.other.org"},
		// "notexample.com" must not match zone "example.com".
		{"example.com", "_acme-challenge.notexample.com.", "_acme-challenge.notexample.com"},
	}
	for _, tt := range tests {
		if got := getSubDomain(tt.domain, tt.fqdn); got != tt.want {
			t.Errorf("getSubDomain(%q, %q) = %q, want %q", tt.domain, tt.fqdn, got, tt.want)
		}
	}
}

func TestLoadConfigAndValidate(t *testing.T) {
	cfg, err := loadConfig(&extapi.JSON{Raw: []byte(`{"apiKey":{"name":"pb","key":"k"},"secretApiKey":{"name":"pb","key":"s"}}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := validate(&cfg); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}

	if _, err := loadConfig(&extapi.JSON{Raw: []byte(`{`)}); err == nil {
		t.Error("expected decode error")
	}

	empty, _ := loadConfig(nil)
	err = validate(&empty)
	if err == nil || !strings.Contains(err.Error(), "apiKey") || !strings.Contains(err.Error(), "secretApiKey") {
		t.Errorf("validate(empty) = %v, want both fields reported", err)
	}
}

func TestSecret(t *testing.T) {
	p := &porkbunDNSProviderSolver{client: fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "pb", Namespace: "cert-manager"},
		Data: map[string][]byte{
			"k":     []byte("pk1_abc\n"),
			"blank": []byte("  "),
		},
	})}
	ctx := context.Background()
	sel := func(name, key string) corev1.SecretKeySelector {
		return corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: name}, Key: key}
	}

	got, err := p.secret(ctx, sel("pb", "k"), "cert-manager")
	if err != nil || got != "pk1_abc" {
		t.Errorf("secret() = %q, %v; want trimmed value", got, err)
	}
	for _, tc := range []struct{ name, key, ns string }{
		{"pb", "missing", "cert-manager"},
		{"pb", "blank", "cert-manager"},
		{"nope", "k", "cert-manager"},
		{"pb", "k", "other-namespace"},
	} {
		if _, err := p.secret(ctx, sel(tc.name, tc.key), tc.ns); err == nil {
			t.Errorf("secret(%s/%s[%s]): expected error", tc.ns, tc.name, tc.key)
		}
	}

	if _, err := (&porkbunDNSProviderSolver{}).secret(ctx, sel("pb", "k"), "cert-manager"); err == nil {
		t.Error("expected error with nil client")
	}
}
