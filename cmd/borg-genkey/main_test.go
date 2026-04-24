package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/undy-io/BORG/internal/auth"
	"github.com/undy-io/BORG/internal/config"
)

func TestRunUsesConfigMapDefaults(t *testing.T) {
	key := []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	client := fake.NewSimpleClientset(
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "borg-config", Namespace: "models"},
			Data: map[string]string{
				"config.yaml": `
borg:
  auth_key_from_env: BORG_AUTH_KEY
  auth_prefix: "BORG:"
`,
			},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "borg-auth", Namespace: "models"},
			Data: map[string][]byte{
				"BORG_AUTH_KEY": []byte(base64.URLEncoding.EncodeToString(key)),
			},
		},
	)

	stdout, stderr := runGenkey(t, client, options{
		username:        "alice",
		namespace:       "models",
		release:         "borg",
		secretSuffix:    defaultSecretSuffix,
		configMapSuffix: defaultConfigMapSuffix,
	})

	assertTokenUsername(t, strings.TrimSpace(stdout), key, "BORG:", "alice")
	if stderr != "" {
		t.Fatalf("expected empty stderr, got %q", stderr)
	}
}

func TestRunUsesFlagOverrides(t *testing.T) {
	key := []byte("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	client := fake.NewSimpleClientset(
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "borg-config", Namespace: "models"},
			Data: map[string]string{
				"config.yaml": `
borg:
  auth_key_from_env: WRONG_KEY
  auth_prefix: "WRONG:"
`,
			},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "borg-auth", Namespace: "models"},
			Data: map[string][]byte{
				"OVERRIDE_KEY": key,
			},
		},
	)

	stdout, _ := runGenkey(t, client, options{
		username:        "bob",
		namespace:       "models",
		release:         "borg",
		keyName:         "OVERRIDE_KEY",
		authPrefix:      "CUSTOM:",
		secretSuffix:    defaultSecretSuffix,
		configMapSuffix: defaultConfigMapSuffix,
	})

	assertTokenUsername(t, strings.TrimSpace(stdout), key, "CUSTOM:", "bob")
}

func TestRunDefaultsPrefixAndUsesFirstSecretKey(t *testing.T) {
	key := []byte("cccccccccccccccccccccccccccccccc")
	client := fake.NewSimpleClientset(
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "borg-auth", Namespace: "models"},
			Data: map[string][]byte{
				"B_KEY": []byte("not-a-valid-key"),
				"A_KEY": key,
			},
		},
	)

	stdout, stderr := runGenkey(t, client, options{
		username:        "carol",
		namespace:       "models",
		release:         "borg",
		secretSuffix:    defaultSecretSuffix,
		configMapSuffix: defaultConfigMapSuffix,
	})

	assertTokenUsername(t, strings.TrimSpace(stdout), key, config.DefaultAuthPrefix, "carol")
	if !strings.Contains(stderr, `using key "A_KEY"`) {
		t.Fatalf("expected stderr to mention chosen key, got %q", stderr)
	}
}

func TestRunErrorsWhenSecretKeyIsMissing(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "borg-auth", Namespace: "models"},
			Data: map[string][]byte{
				"BORG_AUTH_KEY": []byte("not-this-one"),
			},
		},
	)

	var stdout, stderr bytes.Buffer
	err := run(context.Background(), client, options{
		username:        "dora",
		namespace:       "models",
		release:         "borg",
		keyName:         "MISSING",
		secretSuffix:    defaultSecretSuffix,
		configMapSuffix: defaultConfigMapSuffix,
	}, &stdout, &stderr)

	if err == nil {
		t.Fatal("expected missing key error")
	}
	if !strings.Contains(err.Error(), `key "MISSING" not found`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseOptionsAcceptsInterspersedFlags(t *testing.T) {
	var stderr bytes.Buffer
	tests := [][]string{
		{"alice", "-n", "models", "-r", "borg"},
		{"-n", "models", "-r", "borg", "alice"},
		{"-n", "models", "alice", "-r", "borg"},
		{"--namespace", "models", "alice", "--release", "borg"},
		{"--key-name", "AUTH_KEY", "alice", "--namespace", "models", "--release", "borg", "--auth-prefix", "PROXY:"},
		{"--namespace=models", "alice", "--release=borg"},
	}

	for _, args := range tests {
		opts, err := parseOptions(args, &stderr)
		if err != nil {
			t.Fatalf("expected valid options for %v: %v", args, err)
		}
		if opts.username != "alice" || opts.namespace != "models" || opts.release != "borg" {
			t.Fatalf("unexpected parsed options for %v: %#v", args, opts)
		}
	}
}

func TestParseOptionsRequiresUsernameNamespaceAndRelease(t *testing.T) {
	var stderr bytes.Buffer
	if _, err := parseOptions([]string{"alice", "bob", "-n", "models", "-r", "borg"}, &stderr); err == nil {
		t.Fatal("expected multiple username error")
	}
	if _, err := parseOptions([]string{"-n", "models", "-r", "borg"}, &stderr); err == nil {
		t.Fatal("expected username error")
	}
	if _, err := parseOptions([]string{"alice", "-r", "borg"}, &stderr); err == nil {
		t.Fatal("expected namespace error")
	}
	if _, err := parseOptions([]string{"alice", "-n", "models"}, &stderr); err == nil {
		t.Fatal("expected release error")
	}
}

func runGenkey(t *testing.T, client *fake.Clientset, opts options) (string, string) {
	t.Helper()

	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), client, opts, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	return stdout.String(), stderr.String()
}

func assertTokenUsername(t *testing.T, token string, key []byte, prefix string, wantUsername string) {
	t.Helper()

	authenticator, err := auth.New(base64.URLEncoding.EncodeToString(key), prefix)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	username, err := authenticator.Require(req)
	if err != nil {
		t.Fatal(err)
	}
	if username != wantUsername {
		t.Fatalf("expected username %q, got %q", wantUsername, username)
	}
}
