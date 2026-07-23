// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"strings"
	"testing"
	"time"
)

func makeCert(t *testing.T, ecdsaKey bool) (*x509.Certificate, any) {
	t.Helper()
	var pub, priv any
	if ecdsaKey {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		priv, pub = k, &k.PublicKey
	} else {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		priv, pub = k, &k.PublicKey
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageEmailProtection},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, priv
}

func TestSignMIME_producesMultipartSigned(t *testing.T) {
	cert, key := makeCert(t, false)
	body := []byte("From: a@b\r\nTo: c@d\r\nSubject: hi\r\n\r\nhello")
	signed, err := SignMIME(body, SMIMESigner{Certificate: cert, PrivateKey: key})
	if err != nil {
		t.Fatal(err)
	}
	s := string(signed)
	if !strings.Contains(s, "multipart/signed") {
		t.Errorf("missing multipart/signed envelope:\n%s", s)
	}
	if !strings.Contains(s, "application/pkcs7-signature") {
		t.Errorf("missing pkcs7-signature part")
	}
	if !strings.Contains(s, "smime.p7s") {
		t.Errorf("missing smime.p7s filename")
	}
}

func TestEncryptMIME_producesEnvelopedData(t *testing.T) {
	cert, _ := makeCert(t, false)
	body := []byte("plain content")
	enc, err := EncryptMIME(body, []*x509.Certificate{cert})
	if err != nil {
		t.Fatal(err)
	}
	s := string(enc)
	if !strings.Contains(s, "smime-type=enveloped-data") {
		t.Errorf("missing enveloped-data:\n%s", s)
	}
	if !strings.Contains(s, "smime.p7m") {
		t.Errorf("missing smime.p7m filename")
	}
}

func TestSignAndEncryptMIME(t *testing.T) {
	cert, key := makeCert(t, false)
	rcptCert, _ := makeCert(t, false)
	body := []byte("hello")
	out, err := SignAndEncryptMIME(body, SMIMESigner{Certificate: cert, PrivateKey: key},
		[]*x509.Certificate{rcptCert})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "smime-type=enveloped-data") {
		t.Errorf("expected enveloped-data wrapper")
	}
}

func TestSignMIME_validation(t *testing.T) {
	if _, err := SignMIME([]byte("x"), SMIMESigner{}); err == nil {
		t.Error("want error with no signer")
	}
}

func TestSignMIME_includesIntermediates(t *testing.T) {
	leaf, leafKey := makeCert(t, false)
	intCA, _ := makeCert(t, false)
	signed, err := SignMIME([]byte("hi"), SMIMESigner{
		Certificate:   leaf,
		PrivateKey:    leafKey,
		Intermediates: []*x509.Certificate{intCA},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(signed), "multipart/signed") {
		t.Error("missing multipart/signed envelope")
	}
}

func TestSignAndEncryptMIME_signerErrorPropagates(t *testing.T) {
	rcptCert, _ := makeCert(t, false)
	// Empty signer fails the SignMIME validation; SignAndEncryptMIME must
	// surface that error rather than continue to encrypt.
	_, err := SignAndEncryptMIME([]byte("x"), SMIMESigner{}, []*x509.Certificate{rcptCert})
	if err == nil || !strings.Contains(err.Error(), "Certificate") {
		t.Errorf("err = %v", err)
	}
}

func TestEncryptMIME_validation(t *testing.T) {
	if _, err := EncryptMIME([]byte("x"), nil); err == nil {
		t.Error("want error with no recipients")
	}
}

func TestBase64Wrap_lineLength(t *testing.T) {
	in := make([]byte, 200)
	for i := range in {
		in[i] = 'A'
	}
	out := base64Wrap(in)
	for line := range strings.SplitSeq(out, "\r\n") {
		if len(line) > 76 {
			t.Errorf("line too long: %d", len(line))
		}
	}
}
