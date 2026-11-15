// Package porkbun is a minimal client for the Porkbun DNS API v3
// (https://porkbun.com/api/json/v3/documentation).
//
// Originally written by Brandon Sprague (github.com/bcspragu/cert-manager-webhook-porkbun)
// and maintained by Talinx (github.com/Talinx/cert-manager-webhook-porkbun);
// this repository is a clone of the latter.
package porkbun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// DefaultBaseURL is the Porkbun API v3 endpoint.
	DefaultBaseURL = "https://api.porkbun.com/api/json/v3/"

	// StatusSuccess is the value of the "status" field on successful calls.
	StatusSuccess = "SUCCESS"

	defaultTimeout = 30 * time.Second
	// maxResponseBytes bounds how much of a response body is read.
	maxResponseBytes = 1 << 20
	// maxErrorBodyBytes bounds how much of an unexpected body is echoed in errors.
	maxErrorBodyBytes = 256
)

// Client talks to the Porkbun API. Create one with New.
type Client struct {
	secretAPIKey string
	apiKey       string
	baseURL      string
	http         *http.Client
}

// Option customises a Client.
type Option func(*Client)

// WithBaseURL overrides the API endpoint (used in tests).
func WithBaseURL(u string) Option {
	return func(c *Client) {
		if !strings.HasSuffix(u, "/") {
			u += "/"
		}
		c.baseURL = u
	}
}

// WithHTTPClient overrides the HTTP client.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.http = h }
}

// New returns a Client authenticated with the given API key pair.
func New(secretAPIKey, apiKey string, opts ...Option) *Client {
	c := &Client{
		secretAPIKey: secretAPIKey,
		apiKey:       apiKey,
		baseURL:      DefaultBaseURL,
		http:         &http.Client{Timeout: defaultTimeout},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// BaseRequest carries the credentials every Porkbun call requires.
type BaseRequest struct {
	SecretAPIKey string `json:"secretapikey"`
	APIKey       string `json:"apikey"`
}

// Response holds the fields common to all Porkbun responses.
type Response struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

// StatusError is returned when Porkbun answers with a non-SUCCESS status.
type StatusError struct {
	Op      string
	Status  string
	Message string
}

func (e *StatusError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("porkbun %s: status %q: %s", e.Op, e.Status, e.Message)
	}
	return fmt.Sprintf("porkbun %s: status %q", e.Op, e.Status)
}

func (r Response) check(op string) error {
	if r.Status != StatusSuccess {
		return &StatusError{Op: op, Status: r.Status, Message: r.Message}
	}
	return nil
}

type PingResponse struct {
	Response
	IP string `json:"yourIp"`
}

// Ping verifies the credentials.
func (c *Client) Ping(ctx context.Context) (*PingResponse, error) {
	var resp PingResponse
	if err := c.post(ctx, "ping", c.baseRequest(), &resp); err != nil {
		return nil, err
	}
	return &resp, resp.check("ping")
}

func (c *Client) baseRequest() *BaseRequest {
	return &BaseRequest{
		SecretAPIKey: c.secretAPIKey,
		APIKey:       c.apiKey,
	}
}

type RetrieveDNSRecordsResponse struct {
	Response
	Records []DNSRecord `json:"records"`
}

type DNSRecord struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Content  string `json:"content"`
	TTL      string `json:"ttl"`
	Priority string `json:"prio"`
	Notes    string `json:"notes"`
}

// RetrieveDNSRecordsByDomain lists every record in domain.
func (c *Client) RetrieveDNSRecordsByDomain(ctx context.Context, domain string) (*RetrieveDNSRecordsResponse, error) {
	p, err := buildPath("dns/retrieve", domain)
	if err != nil {
		return nil, err
	}
	return c.retrieveDNSRecords(ctx, p)
}

// RetrieveDNSRecordsByDomainSubdomainType lists records of type typ for
// subdomain in domain. An empty subdomain means the apex.
func (c *Client) RetrieveDNSRecordsByDomainSubdomainType(ctx context.Context, domain, subdomain, typ string) (*RetrieveDNSRecordsResponse, error) {
	p, err := buildPath("dns/retrieveByNameType", domain, typ)
	if err != nil {
		return nil, err
	}
	if subdomain != "" {
		if p, err = appendSegment(p, subdomain); err != nil {
			return nil, err
		}
	}
	return c.retrieveDNSRecords(ctx, p)
}

func (c *Client) retrieveDNSRecords(ctx context.Context, path string) (*RetrieveDNSRecordsResponse, error) {
	var resp RetrieveDNSRecordsResponse
	if err := c.post(ctx, path, c.baseRequest(), &resp); err != nil {
		return nil, err
	}
	return &resp, resp.check("retrieve DNS records")
}

type NewDNSRecord struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Content  string `json:"content"`
	TTL      string `json:"ttl"`
	Priority string `json:"prio,omitempty"`
}

type CreateDNSRecordRequest struct {
	*BaseRequest
	*NewDNSRecord
}

type CreateDNSRecordResponse struct {
	Response
	// Porkbun has returned the ID both as a number and as a string.
	ID json.Number `json:"id"`
}

// CreateDNSRecord adds record to domain.
func (c *Client) CreateDNSRecord(ctx context.Context, domain string, record *NewDNSRecord) (*CreateDNSRecordResponse, error) {
	if record == nil {
		return nil, errors.New("porkbun: nil record")
	}
	p, err := buildPath("dns/create", domain)
	if err != nil {
		return nil, err
	}
	var resp CreateDNSRecordResponse
	if err := c.post(ctx, p, &CreateDNSRecordRequest{BaseRequest: c.baseRequest(), NewDNSRecord: record}, &resp); err != nil {
		return nil, err
	}
	return &resp, resp.check("create DNS record")
}

type DeleteDNSRecordResponse struct {
	Response
}

// DeleteDNSRecordByDomainID deletes record id from domain.
func (c *Client) DeleteDNSRecordByDomainID(ctx context.Context, domain, id string) (*DeleteDNSRecordResponse, error) {
	p, err := buildPath("dns/delete", domain, id)
	if err != nil {
		return nil, err
	}
	var resp DeleteDNSRecordResponse
	if err := c.post(ctx, p, c.baseRequest(), &resp); err != nil {
		return nil, err
	}
	return &resp, resp.check("delete DNS record")
}

// post JSON-encodes body, POSTs it to path and decodes the reply into out.
// A non-2xx HTTP status is returned as an error unless the body still decodes
// as a Porkbun response (Porkbun reports API errors as JSON with 400).
func (c *Client) post(ctx context.Context, path string, body, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("porkbun: encode request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("porkbun: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		// Do not wrap the URL error with the request body: it holds the keys.
		return fmt.Errorf("porkbun: POST %s: %w", path, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("porkbun: read response: %w", err)
	}

	decodeErr := json.Unmarshal(raw, out)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		if decodeErr == nil {
			// Let the caller report the Porkbun status/message.
			return nil
		}
		return fmt.Errorf("porkbun: POST %s: HTTP %d: %s", path, resp.StatusCode, truncate(raw, maxErrorBodyBytes))
	}
	if decodeErr != nil {
		return fmt.Errorf("porkbun: decode response from %s: %w", path, decodeErr)
	}
	return nil
}

// buildPath joins prefix with URL-escaped segments, rejecting values that
// could change the request target.
func buildPath(prefix string, segments ...string) (string, error) {
	p := prefix
	for _, s := range segments {
		var err error
		if p, err = appendSegment(p, s); err != nil {
			return "", err
		}
	}
	return p, nil
}

func appendSegment(p, s string) (string, error) {
	if s == "" || s == "." || s == ".." || strings.ContainsAny(s, "/\\") {
		return "", fmt.Errorf("porkbun: invalid path segment %q", s)
	}
	return p + "/" + url.PathEscape(s), nil
}

func truncate(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
