package main

import (
	"crypto/tls"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type certificateReloader struct {
	certFile string
	keyFile  string

	mu      sync.Mutex
	cert    *tls.Certificate
	certSig fileSignature
	keySig  fileSignature
}

type fileSignature struct {
	resolvedPath string
	size         int64
	modTime      time.Time
}

func newReloadingTLSConfig(files tlsFiles) (*tls.Config, error) {
	reloader, err := newCertificateReloader(files)
	if err != nil {
		return nil, err
	}

	return &tls.Config{
		MinVersion:     tls.VersionTLS12,
		GetCertificate: reloader.getCertificate,
	}, nil
}

func newCertificateReloader(files tlsFiles) (*certificateReloader, error) {
	reloader := &certificateReloader{
		certFile: files.certFile,
		keyFile:  files.keyFile,
	}

	if err := reloader.reloadLocked(); err != nil {
		return nil, err
	}
	return reloader, nil
}

func (r *certificateReloader) getCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	changed, err := r.filesChangedLocked()
	if err != nil {
		log.Printf("BORG TLS certificate stat failed; using previous certificate: %v", err)
		return r.cert, nil
	}

	if changed {
		if err := r.reloadLocked(); err != nil {
			log.Printf("BORG TLS certificate reload failed; using previous certificate: %v", err)
			return r.cert, nil
		}
		log.Printf("BORG TLS certificate reloaded")
	}

	return r.cert, nil
}

func (r *certificateReloader) filesChangedLocked() (bool, error) {
	certSig, err := statFileSignature(r.certFile)
	if err != nil {
		return false, err
	}
	keySig, err := statFileSignature(r.keyFile)
	if err != nil {
		return false, err
	}

	return certSig != r.certSig || keySig != r.keySig, nil
}

func (r *certificateReloader) reloadLocked() error {
	cert, certSig, keySig, err := loadCertificate(r.certFile, r.keyFile)
	if err != nil {
		return err
	}

	r.cert = cert
	r.certSig = certSig
	r.keySig = keySig
	return nil
}

func loadCertificate(certFile string, keyFile string) (*tls.Certificate, fileSignature, fileSignature, error) {
	certSig, err := statFileSignature(certFile)
	if err != nil {
		return nil, fileSignature{}, fileSignature{}, err
	}
	keySig, err := statFileSignature(keyFile)
	if err != nil {
		return nil, fileSignature{}, fileSignature{}, err
	}

	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fileSignature{}, fileSignature{}, err
	}

	return &cert, certSig, keySig, nil
}

func statFileSignature(path string) (fileSignature, error) {
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fileSignature{}, err
	}

	info, err := os.Stat(path)
	if err != nil {
		return fileSignature{}, err
	}

	return fileSignature{
		resolvedPath: resolvedPath,
		size:         info.Size(),
		modTime:      info.ModTime(),
	}, nil
}
