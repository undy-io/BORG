package main

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestResolveTLSFilesDefaultsToHTTP(t *testing.T) {
	t.Setenv(tlsCertFileEnv, "")
	t.Setenv(tlsKeyFileEnv, "")

	files, err := resolveTLSFiles("", "")
	if err != nil {
		t.Fatal(err)
	}
	if files.enabled() {
		t.Fatalf("expected TLS to be disabled, got %#v", files)
	}
}

func TestResolveTLSFilesUsesEnvironment(t *testing.T) {
	t.Setenv(tlsCertFileEnv, "/tls/tls.crt")
	t.Setenv(tlsKeyFileEnv, "/tls/tls.key")

	files, err := resolveTLSFiles("", "")
	if err != nil {
		t.Fatal(err)
	}
	if files.certFile != "/tls/tls.crt" || files.keyFile != "/tls/tls.key" {
		t.Fatalf("unexpected TLS files: %#v", files)
	}
}

func TestResolveTLSFilesFlagsOverrideEnvironment(t *testing.T) {
	t.Setenv(tlsCertFileEnv, "/env/tls.crt")
	t.Setenv(tlsKeyFileEnv, "/env/tls.key")

	files, err := resolveTLSFiles("/flag/tls.crt", "/flag/tls.key")
	if err != nil {
		t.Fatal(err)
	}
	if files.certFile != "/flag/tls.crt" || files.keyFile != "/flag/tls.key" {
		t.Fatalf("unexpected TLS files: %#v", files)
	}
}

func TestResolveTLSFilesRequiresCertificateAndKey(t *testing.T) {
	t.Setenv(tlsCertFileEnv, "")
	t.Setenv(tlsKeyFileEnv, "")

	for _, tt := range []struct {
		name     string
		certFile string
		keyFile  string
	}{
		{name: "missing key", certFile: "/tls/tls.crt"},
		{name: "missing cert", keyFile: "/tls/tls.key"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := resolveTLSFiles(tt.certFile, tt.keyFile)
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestNewServerRunnerSelectsHTTPByDefault(t *testing.T) {
	runner, err := newServerRunner(&http.Server{}, tlsFiles{})
	if err != nil {
		t.Fatal(err)
	}
	if runner.scheme != "http" {
		t.Fatalf("expected http scheme, got %q", runner.scheme)
	}
}

func TestNewServerRunnerSelectsHTTPSWithTLSFiles(t *testing.T) {
	certFile, keyFile := writeTestCertificate(t, t.TempDir(), "borg.test")

	runner, err := newServerRunner(&http.Server{}, tlsFiles{certFile: certFile, keyFile: keyFile})
	if err != nil {
		t.Fatal(err)
	}
	if runner.scheme != "https" {
		t.Fatalf("expected https scheme, got %q", runner.scheme)
	}
}

func TestRunHTTPServerReturnsListenError(t *testing.T) {
	want := errors.New("listen failed")
	err := runHTTPServer(context.Background(), serverRunner{
		listenAndServe: func() error {
			return want
		},
		shutdown: func(context.Context) error {
			t.Fatal("shutdown should not be called")
			return nil
		},
		close: func() error {
			t.Fatal("close should not be called")
			return nil
		},
	})

	if !errors.Is(err, want) {
		t.Fatalf("expected listen error %v, got %v", want, err)
	}
}

func TestRunHTTPServerTreatsServerClosedAsSuccess(t *testing.T) {
	err := runHTTPServer(context.Background(), serverRunner{
		listenAndServe: func() error {
			return http.ErrServerClosed
		},
		shutdown: func(context.Context) error {
			t.Fatal("shutdown should not be called")
			return nil
		},
		close: func() error {
			t.Fatal("close should not be called")
			return nil
		},
	})

	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestRunHTTPServerCancellationCallsGracefulShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	listenDone := make(chan struct{})
	shutdownCalled := make(chan struct{}, 1)
	closeCalled := make(chan struct{}, 1)

	errCh := make(chan error, 1)
	go func() {
		errCh <- runHTTPServer(ctx, serverRunner{
			listenAndServe: func() error {
				<-listenDone
				return http.ErrServerClosed
			},
			shutdown: func(context.Context) error {
				shutdownCalled <- struct{}{}
				close(listenDone)
				return nil
			},
			close: func() error {
				closeCalled <- struct{}{}
				return nil
			},
			timeout: time.Second,
		})
	}()

	cancel()

	select {
	case <-shutdownCalled:
	case <-time.After(time.Second):
		t.Fatal("shutdown was not called")
	}

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("runHTTPServer did not return")
	}

	select {
	case <-closeCalled:
		t.Fatal("close should not be called")
	default:
	}
}

func TestRunHTTPServerShutdownFailureCallsCloseFallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	shutdownErr := errors.New("shutdown failed")
	listenDone := make(chan struct{})
	closeCalled := make(chan struct{}, 1)

	errCh := make(chan error, 1)
	go func() {
		errCh <- runHTTPServer(ctx, serverRunner{
			listenAndServe: func() error {
				<-listenDone
				return http.ErrServerClosed
			},
			shutdown: func(context.Context) error {
				return shutdownErr
			},
			close: func() error {
				closeCalled <- struct{}{}
				close(listenDone)
				return nil
			},
			timeout: time.Second,
		})
	}()

	cancel()

	select {
	case <-closeCalled:
	case <-time.After(time.Second):
		t.Fatal("close fallback was not called")
	}

	select {
	case err := <-errCh:
		if !errors.Is(err, shutdownErr) {
			t.Fatalf("expected shutdown error %v, got %v", shutdownErr, err)
		}
	case <-time.After(time.Second):
		t.Fatal("runHTTPServer did not return")
	}
}
