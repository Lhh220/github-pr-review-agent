package github

import (
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

var ErrMissingAppConfig = errors.New("github app config incomplete")

type AppAuth struct {
	AppID          string
	InstallationID string
	PrivateKeyPath string
}

func LoadPrivateKey(path string) (*rsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read private key: %w", err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("invalid private key pem")
	}
	switch block.Type {
	case "RSA PRIVATE KEY":
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	case "PRIVATE KEY":
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		rsaKey, ok := key.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("private key is not RSA")
		}
		return rsaKey, nil
	default:
		return nil, fmt.Errorf("unsupported private key type: %s", block.Type)
	}
}

func CreateAppJWT(appID string, key *rsa.PrivateKey) (string, error) {
	now := time.Now().Unix()
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	claims := map[string]any{"iat": now, "exp": now + 600, "iss": appID}
	return signJWT(header, claims, key)
}

func signJWT(header map[string]string, claims map[string]any, key *rsa.PrivateKey) (string, error) {
	h, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	c, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	h64 := base64.RawURLEncoding.EncodeToString(h)
	c64 := base64.RawURLEncoding.EncodeToString(c)
	signingInput := h64 + "." + c64
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	sig64 := base64.RawURLEncoding.EncodeToString(sig)
	return signingInput + "." + sig64, nil
}

func GetInstallationToken(auth AppAuth) (string, error) {
	if auth.AppID == "" || auth.InstallationID == "" || auth.PrivateKeyPath == "" {
		return "", ErrMissingAppConfig
	}
	key, err := LoadPrivateKey(auth.PrivateKeyPath)
	if err != nil {
		return "", err
	}
	jwt, err := CreateAppJWT(auth.AppID, key)
	if err != nil {
		return "", err
	}
	url := "https://api.github.com/app/installations/" + auth.InstallationID + "/access_tokens"
	req, err := http.NewRequest(http.MethodPost, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("get installation token: status=%d body=%s", resp.StatusCode, string(body))
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.Token == "" {
		return "", errors.New("empty installation token")
	}
	return out.Token, nil
}

func VerifyWebhookSignature(secret string, body []byte, signature string) bool {
	if secret == "" {
		return true
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}
