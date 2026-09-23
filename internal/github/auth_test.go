package github

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
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

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Hold the connection open until the client gives up.
		<-r.Context().Done()
	}))
	defer server.Close()

	original := authHTTPClient
	authHTTPClient = &http.Client{Timeout: 100 * time.Millisecond}
	defer func() { authHTTPClient = original }()

	started := time.Now()
	_, err = CreateInstallationToken(AppAuth{
		AppID:          "123",
		InstallationID: "456",
		PrivateKey:     string(keyPEM),
	})
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("hung token exchange must fail")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("token exchange took %v, want a fast timeout failure", elapsed)
	}
}
