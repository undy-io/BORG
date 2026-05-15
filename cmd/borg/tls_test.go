package main

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCertificateReloaderLoadsInitialCertificate(t *testing.T) {
	certFile, keyFile := writeTestCertificate(t, t.TempDir(), "initial.borg.test")

	reloader, err := newCertificateReloader(tlsFiles{certFile: certFile, keyFile: keyFile})
	if err != nil {
		t.Fatal(err)
	}

	cert := currentTestCertificate(t, reloader)
	if got := certificateCommonName(t, cert); got != "initial.borg.test" {
		t.Fatalf("expected initial certificate, got %q", got)
	}
}

func TestCertificateReloaderFailsOnMissingOrInvalidCertificate(t *testing.T) {
	t.Run("missing key", func(t *testing.T) {
		certFile, _ := writeTestCertificate(t, t.TempDir(), "missing-key.borg.test")

		_, err := newCertificateReloader(tlsFiles{certFile: certFile, keyFile: certFile + ".missing"})
		if err == nil {
			t.Fatal("expected missing key error")
		}
	})

	t.Run("invalid cert", func(t *testing.T) {
		dir := t.TempDir()
		certFile := filepath.Join(dir, "tls.crt")
		keyFile := filepath.Join(dir, "tls.key")
		if err := os.WriteFile(certFile, []byte("not a certificate"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(keyFile, []byte("not a key"), 0o600); err != nil {
			t.Fatal(err)
		}

		_, err := newCertificateReloader(tlsFiles{certFile: certFile, keyFile: keyFile})
		if err == nil {
			t.Fatal("expected invalid certificate error")
		}
	})
}

func TestCertificateReloaderReloadsUpdatedCertificate(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := writeTestCertificate(t, dir, "initial.borg.test")

	reloader, err := newCertificateReloader(tlsFiles{certFile: certFile, keyFile: keyFile})
	if err != nil {
		t.Fatal(err)
	}

	writeTestCertificate(t, dir, "updated.borg.test")
	touchTestCertificateFiles(t, certFile, keyFile)

	cert := currentTestCertificate(t, reloader)
	if got := certificateCommonName(t, cert); got != "updated.borg.test" {
		t.Fatalf("expected updated certificate, got %q", got)
	}
}

func TestCertificateReloaderReloadsSymlinkSwappedCertificate(t *testing.T) {
	dir := t.TempDir()
	versionOne := filepath.Join(dir, "version-one")
	versionTwo := filepath.Join(dir, "version-two")
	if err := os.Mkdir(versionOne, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(versionTwo, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestCertificate(t, versionOne, "initial.borg.test")
	writeTestCertificate(t, versionTwo, "updated.borg.test")

	dataLink := filepath.Join(dir, "..data")
	certFile := filepath.Join(dir, "tls.crt")
	keyFile := filepath.Join(dir, "tls.key")
	createTestSymlink(t, versionOne, dataLink)
	createTestSymlink(t, filepath.Join("..data", "tls.crt"), certFile)
	createTestSymlink(t, filepath.Join("..data", "tls.key"), keyFile)

	reloader, err := newCertificateReloader(tlsFiles{certFile: certFile, keyFile: keyFile})
	if err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(dataLink); err != nil {
		t.Fatal(err)
	}
	createTestSymlink(t, versionTwo, dataLink)

	cert := currentTestCertificate(t, reloader)
	if got := certificateCommonName(t, cert); got != "updated.borg.test" {
		t.Fatalf("expected symlink-swapped certificate, got %q", got)
	}
}

func TestCertificateReloaderKeepsPreviousCertificateAfterBrokenReplacement(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := writeTestCertificate(t, dir, "stable.borg.test")

	reloader, err := newCertificateReloader(tlsFiles{certFile: certFile, keyFile: keyFile})
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(certFile, []byte("broken certificate"), 0o600); err != nil {
		t.Fatal(err)
	}

	cert := currentTestCertificate(t, reloader)
	if got := certificateCommonName(t, cert); got != "stable.borg.test" {
		t.Fatalf("expected previous certificate after broken replacement, got %q", got)
	}
}

func TestReloadingTLSConfigServesHTTPS(t *testing.T) {
	certFile, keyFile := writeTestCertificate(t, t.TempDir(), "https.borg.test")
	tlsConfig, err := newReloadingTLSConfig(tlsFiles{certFile: certFile, keyFile: keyFile})
	if err != nil {
		t.Fatal(err)
	}

	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	deadline := time.Now().Add(5 * time.Second)
	if err := serverConn.SetDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	if err := clientConn.SetDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	serverTLS := tls.Server(serverConn, tlsConfig)
	clientTLS := tls.Client(clientConn, &tls.Config{InsecureSkipVerify: true})

	serverErr := make(chan error, 1)
	go func() {
		defer serverTLS.Close()

		if err := serverTLS.Handshake(); err != nil {
			serverErr <- err
			return
		}

		req, err := http.ReadRequest(bufio.NewReader(serverTLS))
		if err != nil {
			serverErr <- err
			return
		}
		_ = req.Body.Close()

		_, err = io.WriteString(serverTLS, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")
		serverErr <- err
	}()

	if err := clientTLS.Handshake(); err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequest(http.MethodGet, "https://borg.test/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := req.Write(clientTLS); err != nil {
		t.Fatal(err)
	}

	resp, err := http.ReadResponse(bufio.NewReader(clientTLS), req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "ok" {
		t.Fatalf("expected ok response, got %q", body)
	}

	select {
	case err := <-serverErr:
		if err != nil {
			t.Fatalf("unexpected server error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("TLS server did not finish")
	}
}

func createTestSymlink(t *testing.T, oldname string, newname string) {
	t.Helper()

	if err := os.Symlink(oldname, newname); err != nil {
		if os.IsPermission(err) {
			t.Skipf("symlinks are not permitted: %v", err)
		}
		t.Fatal(err)
	}
}

func currentTestCertificate(t *testing.T, reloader *certificateReloader) *tls.Certificate {
	t.Helper()

	cert, err := reloader.getCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatal(err)
	}
	if cert == nil {
		t.Fatal("expected certificate")
	}
	return cert
}

func certificateCommonName(t *testing.T, cert *tls.Certificate) string {
	t.Helper()

	parsed, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Subject.CommonName
}

func writeTestCertificate(t *testing.T, dir string, commonName string) (string, string) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		t.Fatal(err)
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: commonName,
		},
		NotBefore: time.Now().Add(-time.Minute),
		NotAfter:  time.Now().Add(time.Hour),
		KeyUsage:  x509.KeyUsageDigitalSignature,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}

	certFile := filepath.Join(dir, "tls.crt")
	keyFile := filepath.Join(dir, "tls.key")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(certFile, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

func touchTestCertificateFiles(t *testing.T, certFile string, keyFile string) {
	t.Helper()

	modTime := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(certFile, modTime, modTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(keyFile, modTime, modTime); err != nil {
		t.Fatal(err)
	}
}
