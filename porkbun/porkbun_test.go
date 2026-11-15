package porkbun

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type recorded struct {
	path string
	body map[string]any
}

func newTestServer(t *testing.T, status int, reply string) (*Client, *recorded) {
	t.Helper()
	rec := &recorded{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		rec.path = r.URL.EscapedPath()
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &rec.body)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return New("sk1_secret", "pk1_key", WithBaseURL(srv.URL+"/api/json/v3")), rec
}

func TestRetrieveByNameType(t *testing.T) {
	c, rec := newTestServer(t, http.StatusOK,
		`{"status":"SUCCESS","records":[{"id":"42","name":"_acme-challenge.example.com","type":"TXT","content":"abc"}]}`)

	resp, err := c.RetrieveDNSRecordsByDomainSubdomainType(context.Background(), "example.com", "_acme-challenge", "TXT")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, want := rec.path, "/api/json/v3/dns/retrieveByNameType/example.com/TXT/_acme-challenge"; got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
	if rec.body["apikey"] != "pk1_key" || rec.body["secretapikey"] != "sk1_secret" {
		t.Errorf("credentials not sent correctly: %v", rec.body)
	}
	if len(resp.Records) != 1 || resp.Records[0].ID != "42" {
		t.Errorf("records = %+v", resp.Records)
	}
}

func TestRetrieveApexOmitsSubdomain(t *testing.T) {
	c, rec := newTestServer(t, http.StatusOK, `{"status":"SUCCESS","records":[]}`)
	if _, err := c.RetrieveDNSRecordsByDomainSubdomainType(context.Background(), "example.com", "", "TXT"); err != nil {
		t.Fatal(err)
	}
	if got, want := rec.path, "/api/json/v3/dns/retrieveByNameType/example.com/TXT"; got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
}

func TestCreateDNSRecord(t *testing.T) {
	c, rec := newTestServer(t, http.StatusOK, `{"status":"SUCCESS","id":123456}`)
	resp, err := c.CreateDNSRecord(context.Background(), "example.com", &NewDNSRecord{
		Name: "_acme-challenge", Type: "TXT", Content: "abc", TTL: "600",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID.String() != "123456" {
		t.Errorf("id = %q", resp.ID)
	}
	if rec.path != "/api/json/v3/dns/create/example.com" {
		t.Errorf("path = %q", rec.path)
	}
	for k, want := range map[string]any{"name": "_acme-challenge", "type": "TXT", "content": "abc", "ttl": "600", "apikey": "pk1_key"} {
		if rec.body[k] != want {
			t.Errorf("body[%q] = %v, want %v", k, rec.body[k], want)
		}
	}
	if _, ok := rec.body["prio"]; ok {
		t.Errorf("empty prio should be omitted")
	}
}

func TestCreateDNSRecordStringID(t *testing.T) {
	c, _ := newTestServer(t, http.StatusOK, `{"status":"SUCCESS","id":"123456"}`)
	resp, err := c.CreateDNSRecord(context.Background(), "example.com", &NewDNSRecord{Name: "x", Type: "TXT", Content: "y", TTL: "600"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID.String() != "123456" {
		t.Errorf("id = %q", resp.ID)
	}
}

func TestAPIErrorStatusIsReturned(t *testing.T) {
	// Porkbun reports API errors as JSON, typically with HTTP 400.
	c, _ := newTestServer(t, http.StatusBadRequest, `{"status":"ERROR","message":"Invalid API key."}`)
	_, err := c.DeleteDNSRecordByDomainID(context.Background(), "example.com", "42")
	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v, want *StatusError", err)
	}
	if se.Status != "ERROR" || se.Message != "Invalid API key." {
		t.Errorf("StatusError = %+v", se)
	}
}

func TestNonJSONErrorIncludesHTTPStatus(t *testing.T) {
	c, _ := newTestServer(t, http.StatusBadGateway, "<html>bad gateway</html>")
	_, err := c.Ping(context.Background())
	if err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Fatalf("err = %v, want HTTP 502", err)
	}
}

func TestErrorsDoNotLeakKeys(t *testing.T) {
	c := New("sk1_secret", "pk1_key", WithBaseURL("http://127.0.0.1:1/"))
	_, err := c.Ping(context.Background())
	if err == nil {
		t.Fatal("expected connection error")
	}
	if strings.Contains(err.Error(), "sk1_secret") || strings.Contains(err.Error(), "pk1_key") {
		t.Errorf("error leaks credentials: %v", err)
	}
}

func TestPathSegmentsAreValidated(t *testing.T) {
	c, rec := newTestServer(t, http.StatusOK, `{"status":"SUCCESS"}`)
	ctx := context.Background()
	for _, bad := range []string{"", ".", "..", "a/b", `a\b`} {
		if _, err := c.DeleteDNSRecordByDomainID(ctx, "example.com", bad); err == nil {
			t.Errorf("id %q: expected error", bad)
		}
	}
	// Characters that would alter the URL are escaped, not interpreted.
	if _, err := c.DeleteDNSRecordByDomainID(ctx, "example.com", "1?x=y#z"); err != nil {
		t.Fatal(err)
	}
	if got, want := rec.path, "/api/json/v3/dns/delete/example.com/1%3Fx=y%23z"; got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
}

func TestNilRecord(t *testing.T) {
	c := New("s", "k")
	if _, err := c.CreateDNSRecord(context.Background(), "example.com", nil); err == nil {
		t.Fatal("expected error for nil record")
	}
}
