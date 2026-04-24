package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestNoAuthReturnsAnonymous(t *testing.T) {
	authenticator, err := New("EMPTY", "")
	if err != nil {
		t.Fatal(err)
	}

	username, err := authenticator.Require(&http.Request{Header: http.Header{}})
	if err != nil {
		t.Fatal(err)
	}
	if username != "ANONYMOUS" {
		t.Fatalf("expected ANONYMOUS, got %q", username)
	}
}

func TestRequireValidToken(t *testing.T) {
	key := []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	authenticator, err := New(base64.URLEncoding.EncodeToString(key), "BORG:")
	if err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Authorization", "Bearer "+mintToken(t, key, "BORG:", "alice"))

	username, err := authenticator.Require(req)
	if err != nil {
		t.Fatal(err)
	}
	if username != "alice" {
		t.Fatalf("expected alice, got %q", username)
	}
}

func TestDefaultPrefixIsProxyUppercase(t *testing.T) {
	key := []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	authenticator, err := New(base64.RawURLEncoding.EncodeToString(key), "")
	if err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Authorization", "Bearer "+mintToken(t, key, "PROXY:", "alice"))
	if _, err := authenticator.Require(req); err != nil {
		t.Fatal(err)
	}
}

func TestRequireAuthErrors(t *testing.T) {
	key := []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	authenticator, err := New(base64.URLEncoding.EncodeToString(key), "BORG:")
	if err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest(http.MethodPost, "/", nil)
	_, err = authenticator.Require(req)
	assertAuthError(t, err, http.StatusUnauthorized, "Missing API key")

	req.Header.Set("Authorization", "Token nope")
	_, err = authenticator.Require(req)
	assertAuthError(t, err, http.StatusUnauthorized, "Missing API key")

	req.Header.Set("Authorization", "Bearer malformed")
	_, err = authenticator.Require(req)
	assertAuthError(t, err, http.StatusUnauthorized, "Invalid API key")

	req.Header.Set("Authorization", "Bearer "+mintToken(t, key, "WRONG:", "alice"))
	_, err = authenticator.Require(req)
	assertAuthError(t, err, http.StatusUnauthorized, "Invalid API key")
}

func TestInvalidAuthKey(t *testing.T) {
	if _, err := New(base64.URLEncoding.EncodeToString([]byte("short")), ""); err == nil {
		t.Fatal("expected invalid key error")
	}
}

func TestAuthKeyMustUseURLSafeBase64(t *testing.T) {
	key := []byte{
		0xff, 0xff, 0xff, 0xff,
		0xff, 0xff, 0xff, 0xff,
		0xff, 0xff, 0xff, 0xff,
		0xff, 0xff, 0xff, 0xff,
		0xff, 0xff, 0xff, 0xff,
		0xff, 0xff, 0xff, 0xff,
		0xff, 0xff, 0xff, 0xff,
		0xff, 0xff, 0xff, 0xff,
	}

	if _, err := New(base64.StdEncoding.EncodeToString(key), ""); err == nil {
		t.Fatal("expected standard base64 auth key to be rejected")
	}
	if _, err := New(base64.URLEncoding.EncodeToString(key), ""); err != nil {
		t.Fatalf("expected URL-safe auth key to be accepted: %v", err)
	}
}

func TestDecodeSecretKeySupportsTextAndLegacyRawSecrets(t *testing.T) {
	rawKey := []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	textKey := base64.URLEncoding.EncodeToString(rawKey)

	decoded, err := DecodeSecretKey([]byte(textKey))
	if err != nil {
		t.Fatal(err)
	}
	if string(decoded) != string(rawKey) {
		t.Fatalf("expected text key to decode to raw key")
	}

	decoded, err = DecodeSecretKey(rawKey)
	if err != nil {
		t.Fatal(err)
	}
	if string(decoded) != string(rawKey) {
		t.Fatalf("expected raw key to be returned")
	}
}

func TestMintTokenCanBeValidated(t *testing.T) {
	key := []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	token, err := MintToken("alice", key, "PROXY:")
	if err != nil {
		t.Fatal(err)
	}

	authenticator, err := New(base64.URLEncoding.EncodeToString(key), "PROXY:")
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	username, err := authenticator.Require(req)
	if err != nil {
		t.Fatal(err)
	}
	if username != "alice" {
		t.Fatalf("expected alice, got %q", username)
	}
}

func TestMintTokenRejectsInvalidKeyLength(t *testing.T) {
	_, err := MintToken("alice", []byte("short"), "PROXY:")
	if err == nil {
		t.Fatal("expected invalid key error")
	}
	if !strings.Contains(err.Error(), "32-byte") {
		t.Fatalf("expected key length error, got %v", err)
	}
}

func mintToken(t *testing.T, key []byte, prefix string, username string) string {
	t.Helper()

	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, NonceLen)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	ciphertext := aead.Seal(nil, nonce, []byte(prefix+username), nil)
	return base64.URLEncoding.EncodeToString(append(nonce, ciphertext...))
}

func assertAuthError(t *testing.T, err error, statusCode int, detail string) {
	t.Helper()

	var authErr *HTTPError
	if !errors.As(err, &authErr) {
		t.Fatalf("expected HTTPError, got %T", err)
	}
	if authErr.StatusCode != statusCode || authErr.Detail != detail {
		t.Fatalf("expected %d %q, got %d %q", statusCode, detail, authErr.StatusCode, authErr.Detail)
	}
}
