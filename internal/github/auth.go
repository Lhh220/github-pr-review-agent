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
	"sort"
	"strings"
	"time"
)

var ErrMissingAppConfig = errors.New("github app config incomplete")

type AppAuth struct {
	AppID          string
	InstallationID string
	PrivateKey     string
	PrivateKeyPath string
}

type InstallationToken struct {
	Token               string
	ExpiresAt           time.Time
	Permissions         map[string]string
	RepositorySelection string
	Repositories        []InstallationRepository
}

type InstallationRepository struct {
	FullName string `json:"full_name"`
}

func (t InstallationToken) PermissionSummary() string {
	keys := make([]string, 0, len(t.Permissions))
	for key := range t.Permissions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+t.Permissions[key])
	}
	return strings.Join(parts, ", ")
}

func (t InstallationToken) RepositorySummary() string {
	names := make([]string, 0, len(t.Repositories))
	for _, repo := range t.Repositories {
		names = append(names, repo.FullName)
	}
	return strings.Join(names, ", ")
}

func LoadPrivateKey(path string) (*rsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read private key: %w", err)
	}
	return ParsePrivateKey(data)
}

func ParsePrivateKey(data []byte) (*rsa.PrivateKey, error) {
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
	token, err := CreateInstallationToken(auth)
	if err != nil {
		return "", err
	}
	return token.Token, nil
}

func CreateInstallationToken(auth AppAuth) (*InstallationToken, error) {
	if auth.AppID == "" || auth.InstallationID == "" || (auth.PrivateKey == "" && auth.PrivateKeyPath == "") {
		return nil, ErrMissingAppConfig
	}
	var key *rsa.PrivateKey
	var err error
	if auth.PrivateKey != "" {
		key, err = ParsePrivateKey([]byte(auth.PrivateKey))
		if err != nil {
			return nil, err
		}
	} else {
		key, err = LoadPrivateKey(auth.PrivateKeyPath)
		if err != nil {
			return nil, err
		}
	}
	jwt, err := CreateAppJWT(auth.AppID, key)
	if err != nil {
		return nil, err
	}
	url := "https://api.github.com/app/installations/" + auth.InstallationID + "/access_tokens"
	req, err := http.NewRequest(http.MethodPost, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get installation token: status=%d body=%s", resp.StatusCode, string(body))
	}
	var out struct {
		Token               string                   `json:"token"`
		ExpiresAt           time.Time                `json:"expires_at"`
		Permissions         map[string]string        `json:"permissions"`
		RepositorySelection string                   `json:"repository_selection"`
		Repositories        []InstallationRepository `json:"repositories"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out.Token == "" {
		return nil, errors.New("empty installation token")
	}
	return &InstallationToken{
		Token:               out.Token,
		ExpiresAt:           out.ExpiresAt,
		Permissions:         out.Permissions,
		RepositorySelection: out.RepositorySelection,
		Repositories:        out.Repositories,
	}, nil
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
