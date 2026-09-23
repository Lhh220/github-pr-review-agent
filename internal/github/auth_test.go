package github

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestVerifyWebhookSignature(t *testing.T) {
	secret := "test-secret"
	body := []byte(`{"action":"opened"}`)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	if !VerifyWebhookSignature(secret, body, sig) {
		t.Fatal("expected valid signature")
	}
	if VerifyWebhookSignature(secret, body, "sha256=bad") {
		t.Fatal("expected invalid signature")
	}
	if !VerifyWebhookSignature("", body, "") {
		t.Fatal("expected to skip verification when secret is empty")
	}
}

func TestCreateInstallationTokenTimesOutOnHungUpstream(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})

	received := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- struct{}{}
		// Hold the connection open until the client gives up.
		<-r.Context().Done()
	}))
	defer server.Close()

	original := authHTTPClient
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	authHTTPClient = &http.Client{Timeout: 200 * time.Millisecond, Transport: tokenTestTransport(func(req *http.Request) (*http.Response, error) {
		local := req.Clone(req.Context())
		local.URL.Scheme = target.Scheme
		local.URL.Host = target.Host
		return transport.RoundTrip(local)
	})}
	defer func() { authHTTPClient = original }()

	started := time.Now()
	_, err = CreateInstallationToken(AppAuth{
		AppID:          "123",
		InstallationID: "456",
		PrivateKey:     string(keyPEM),
	})
	elapsed := time.Since(started)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want client timeout, got %v", err)
	}
	select {
	case <-received:
	default:
		t.Fatal("mock upstream was never reached")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("token exchange took %v, want a fast timeout failure", elapsed)
	}
}

type tokenTestTransport func(*http.Request) (*http.Response, error)

func (f tokenTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
