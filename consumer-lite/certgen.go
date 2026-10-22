package main

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/spiffe/go-spiffe/lite/spiffeid"
)

// writeDeliveredIdentities plays the role of the identity provider side of
// filesystem delivery: it mints a CA, one X509-SVID per workload, and writes
// them as PEM files into dir, the way a delivery agent would.
func writeDeliveredIdentities(dir string, ids ...spiffeid.ID) error {
	caCert, caKey, err := newCA()
	if err != nil {
		return err
	}
	if err := writePEM(filepath.Join(dir, "bundle.pem"), "CERTIFICATE", caCert.Raw); err != nil {
		return err
	}

	for _, id := range ids {
		name := filepath.Base(id.Path())
		cert, key, err := newSVID(caCert, caKey, id)
		if err != nil {
			return err
		}
		if err := writePEM(filepath.Join(dir, name+"_svid.pem"), "CERTIFICATE", cert.Raw); err != nil {
			return err
		}
		keyDER, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return err
		}
		if err := writePEM(filepath.Join(dir, name+"_key.pem"), "PRIVATE KEY", keyDER); err != nil {
			return err
		}
	}
	return nil
}

func newCA() (*x509.Certificate, crypto.Signer, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          newSerial(),
		Subject:               pkix.Name{CommonName: "poc-ca"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	cert, err := selfSign(tmpl, key)
	return cert, key, err
}

func newSVID(caCert *x509.Certificate, caKey crypto.Signer, id spiffeid.ID) (*x509.Certificate, crypto.Signer, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: newSerial(),
		URIs:         []*url.URL{id.URL()},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, key.Public(), caKey)
	if err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	return cert, key, err
}

func selfSign(tmpl *x509.Certificate, key crypto.Signer) (*x509.Certificate, error) {
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return nil, err
	}
	return x509.ParseCertificate(der)
}

func newSerial() *big.Int {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		panic(fmt.Sprintf("generating serial: %v", err))
	}
	return serial
}

func writePEM(path, blockType string, der []byte) error {
	return os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der}), 0600)
}
