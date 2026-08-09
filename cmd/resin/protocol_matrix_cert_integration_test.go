//go:build integration

package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type protocolMatrixCertificates struct {
	caCertificatePath     string
	serverCertificatePath string
	serverKeyPath         string
}

func newProtocolMatrixCertificates(t *testing.T) protocolMatrixCertificates {
	t.Helper()
	directory := t.TempDir()
	caKey := newMatrixPrivateKey(t)
	caTemplate := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Resin protocol matrix CA"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER := createMatrixCertificate(t, &caTemplate, &caTemplate, &caKey.PublicKey, caKey)

	serverKey := newMatrixPrivateKey(t)
	serverTemplate := x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "localhost"},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	serverDER := createMatrixCertificate(t, &serverTemplate, &caTemplate, &serverKey.PublicKey, caKey)

	paths := protocolMatrixCertificates{
		caCertificatePath:     filepath.Join(directory, "ca.pem"),
		serverCertificatePath: filepath.Join(directory, "server.pem"),
		serverKeyPath:         filepath.Join(directory, "server-key.pem"),
	}
	writeMatrixPEM(t, paths.caCertificatePath, "CERTIFICATE", caDER)
	writeMatrixPEM(t, paths.serverCertificatePath, "CERTIFICATE", serverDER)
	keyDER, err := x509.MarshalPKCS8PrivateKey(serverKey)
	if err != nil {
		t.Fatalf("marshal server private key: %v", err)
	}
	writeMatrixPEM(t, paths.serverKeyPath, "PRIVATE KEY", keyDER)
	return paths
}

func newMatrixPrivateKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate ECDSA key: %v", err)
	}
	return key
}

func createMatrixCertificate(
	t *testing.T,
	template *x509.Certificate,
	parent *x509.Certificate,
	publicKey any,
	parentKey any,
) []byte {
	t.Helper()
	der, err := x509.CreateCertificate(rand.Reader, template, parent, publicKey, parentKey)
	if err != nil {
		t.Fatalf("create protocol matrix certificate: %v", err)
	}
	return der
}

func writeMatrixPEM(t *testing.T, path, blockType string, der []byte) {
	t.Helper()
	data := pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der})
	if data == nil {
		t.Fatalf("encode %s PEM", blockType)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
