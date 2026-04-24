package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/undy-io/BORG/internal/config"
)

const (
	NonceLen  = 12
	APIPrefix = "Bearer "
)

type HTTPError struct {
	StatusCode int
	Detail     string
}

func (e *HTTPError) Error() string {
	return e.Detail
}

type Authenticator struct {
	key    []byte
	prefix string
}

func New(keyText string, prefix string) (*Authenticator, error) {
	if prefix == "" {
		prefix = config.DefaultAuthPrefix
	}

	if keyText == "" || keyText == config.DefaultKeyValue {
		return &Authenticator{prefix: prefix}, nil
	}

	key, err := DecodeKeyText(keyText)
	if err != nil {
		return nil, err
	}

	return &Authenticator{key: key, prefix: prefix}, nil
}

func DecodeKeyText(keyText string) ([]byte, error) {
	key, err := decodeURLBase64(keyText)
	if err != nil {
		return nil, fmt.Errorf("auth_key must be base64-url encoded: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("auth_key must be 32-byte AES-256 key (got %d)", len(key))
	}
	return append([]byte(nil), key...), nil
}

func DecodeSecretKey(secretData []byte) ([]byte, error) {
	if len(secretData) == 32 {
		return append([]byte(nil), secretData...), nil
	}
	return DecodeKeyText(string(secretData))
}

func MintToken(username string, key []byte, prefix string) (string, error) {
	return mintTokenWithReader(username, key, prefix, rand.Reader)
}

func (a *Authenticator) Require(r *http.Request) (string, error) {
	if a == nil || len(a.key) == 0 {
		return "ANONYMOUS", nil
	}

	header := r.Header.Get("Authorization")
	if header == "" || !strings.HasPrefix(header, APIPrefix) {
		return "", &HTTPError{StatusCode: http.StatusUnauthorized, Detail: "Missing API key"}
	}

	plaintext, err := decryptToken(strings.TrimPrefix(header, APIPrefix), a.key)
	if err != nil {
		return "", &HTTPError{StatusCode: http.StatusUnauthorized, Detail: "Invalid API key"}
	}
	if !strings.HasPrefix(plaintext, a.prefix) {
		return "", &HTTPError{StatusCode: http.StatusUnauthorized, Detail: "Invalid API key"}
	}

	return strings.TrimPrefix(plaintext, a.prefix), nil
}

func decryptToken(token string, key []byte) (string, error) {
	buf, err := decodeURLBase64(token)
	if err != nil {
		return "", err
	}
	if len(buf) <= NonceLen {
		return "", fmt.Errorf("token too short")
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonce, ciphertext := buf[:NonceLen], buf[NonceLen:]
	plaintext, err := aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

func mintTokenWithReader(username string, key []byte, prefix string, random io.Reader) (string, error) {
	if len(key) != 32 {
		return "", fmt.Errorf("auth key must be 32-byte AES-256 key (got %d)", len(key))
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, NonceLen)
	if _, err := io.ReadFull(random, nonce); err != nil {
		return "", err
	}
	ciphertext := aead.Seal(nil, nonce, []byte(prefix+username), nil)
	return base64.URLEncoding.EncodeToString(append(nonce, ciphertext...)), nil
}

func decodeURLBase64(value string) ([]byte, error) {
	if decoded, err := base64.URLEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	return base64.RawURLEncoding.DecodeString(value)
}
