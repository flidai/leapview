package composectl

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

func ensureBundledPostgresCertificates(secretRoot string) error {
	caKeyPath := filepath.Join(secretRoot, "ca.key")
	caKey, err := ensurePostgresPrivateKey(caKeyPath)
	if err != nil {
		return fmt.Errorf("prepare private PostgreSQL CA key: %w", err)
	}
	caCertPath := filepath.Join(secretRoot, "ca.crt")
	caCert, err := ensurePostgresCACertificate(caCertPath, caKey)
	if err != nil {
		return fmt.Errorf("prepare PostgreSQL CA certificate: %w", err)
	}
	serverKeyPath := filepath.Join(secretRoot, "server.key")
	serverKey, err := ensurePostgresPrivateKey(serverKeyPath)
	if err != nil {
		return fmt.Errorf("prepare PostgreSQL server key: %w", err)
	}
	if err := ensurePostgresServerCertificate(filepath.Join(secretRoot, "server.crt"), caCert, caKey, serverKey); err != nil {
		return fmt.Errorf("prepare PostgreSQL server certificate: %w", err)
	}
	return nil
}

func ensurePostgresPrivateKey(path string) (*ecdsa.PrivateKey, error) {
	data, err := readPostgresMaterial(path, 0o600)
	if os.IsNotExist(err) {
		key, generateErr := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if generateErr != nil {
			return nil, generateErr
		}
		der, marshalErr := x509.MarshalPKCS8PrivateKey(key)
		if marshalErr != nil {
			return nil, marshalErr
		}
		contents := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
		data, err = createPostgresFileOnce(path, contents, 0o600)
	}
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, errors.New("private key is not PKCS#8 PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok || key.Curve != elliptic.P256() {
		return nil, errors.New("private key must use ECDSA P-256")
	}
	return key, nil
}

func ensurePostgresCACertificate(path string, key *ecdsa.PrivateKey) (*x509.Certificate, error) {
	data, err := readPostgresMaterial(path, 0o644)
	if os.IsNotExist(err) {
		now := time.Now()
		serial, serialErr := postgresCertificateSerial()
		if serialErr != nil {
			return nil, serialErr
		}
		template := &x509.Certificate{
			SerialNumber:          serial,
			Subject:               pkix.Name{CommonName: "LeapView bundled PostgreSQL CA"},
			NotBefore:             now.Add(-5 * time.Minute),
			NotAfter:              now.AddDate(15, 0, 0),
			IsCA:                  true,
			BasicConstraintsValid: true,
			KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		}
		der, createErr := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
		if createErr != nil {
			return nil, createErr
		}
		data, err = createPostgresFileOnce(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
	}
	if err != nil {
		return nil, err
	}
	certificate, err := parsePostgresCertificate(data)
	if err != nil {
		return nil, err
	}
	if !certificate.IsCA || !certificate.BasicConstraintsValid || certificate.CheckSignatureFrom(certificate) != nil ||
		!publicKeysEqual(certificate.PublicKey, &key.PublicKey) {
		return nil, errors.New("CA certificate does not match the persisted private key")
	}
	if err := certificate.VerifyHostname(bundledPostgresServerName); err == nil {
		return nil, errors.New("CA certificate must not be used as the PostgreSQL server certificate")
	}
	now := time.Now()
	if now.Before(certificate.NotBefore) || !now.Before(certificate.NotAfter) {
		return nil, errors.New("persisted PostgreSQL CA certificate is outside its validity period")
	}
	return certificate, nil
}

func ensurePostgresServerCertificate(path string, ca *x509.Certificate, caKey, serverKey *ecdsa.PrivateKey) error {
	data, err := readPostgresMaterial(path, 0o644)
	if os.IsNotExist(err) {
		now := time.Now()
		serial, serialErr := postgresCertificateSerial()
		if serialErr != nil {
			return serialErr
		}
		template := &x509.Certificate{
			SerialNumber: serial,
			Subject:      pkix.Name{CommonName: bundledPostgresServerName},
			DNSNames:     []string{bundledPostgresServerName},
			NotBefore:    now.Add(-5 * time.Minute),
			NotAfter:     now.AddDate(10, 0, 0),
			KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
			ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}
		der, createErr := x509.CreateCertificate(rand.Reader, template, ca, &serverKey.PublicKey, caKey)
		if createErr != nil {
			return createErr
		}
		data, err = createPostgresFileOnce(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
	}
	if err != nil {
		return err
	}
	certificate, err := parsePostgresCertificate(data)
	if err != nil {
		return err
	}
	if !publicKeysEqual(certificate.PublicKey, &serverKey.PublicKey) || certificate.CheckSignatureFrom(ca) != nil {
		return errors.New("server certificate does not match the persisted CA and key")
	}
	if err := certificate.VerifyHostname(bundledPostgresServerName); err != nil {
		return fmt.Errorf("server certificate does not authenticate the PostgreSQL service hostname: %w", err)
	}
	now := time.Now()
	if now.Before(certificate.NotBefore) || !now.Before(certificate.NotAfter) {
		return errors.New("persisted PostgreSQL server certificate is outside its validity period")
	}
	return nil
}

func parsePostgresCertificate(data []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("certificate is not PEM encoded")
	}
	return x509.ParseCertificate(block.Bytes)
}

func postgresCertificateSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, err
	}
	if serial.Sign() == 0 {
		return big.NewInt(1), nil
	}
	return serial, nil
}

func publicKeysEqual(first, second any) bool {
	firstDER, firstErr := x509.MarshalPKIXPublicKey(first)
	secondDER, secondErr := x509.MarshalPKIXPublicKey(second)
	return firstErr == nil && secondErr == nil && string(firstDER) == string(secondDER)
}
