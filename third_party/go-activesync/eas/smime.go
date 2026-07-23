// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"crypto"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/smallstep/pkcs7"
)

// SMIMESigner identifies the certificate + private key used to sign
// outbound mail. Pass to SignMIME or to SendMailOptions.SMIMESign.
type SMIMESigner struct {
	Certificate *x509.Certificate
	PrivateKey  crypto.PrivateKey
	// Intermediates are CAs to include in the SignedData chain so the
	// recipient can verify without consulting AIA URLs.
	Intermediates []*x509.Certificate
}

// SignMIME wraps mime in a multipart/signed S/MIME envelope using
// PKCS#7 detached signatures (the "smime-type=signed-data" form most
// MUAs render correctly). The returned bytes are a complete RFC 5322
// message body suitable for SendMail.
func SignMIME(mime []byte, signer SMIMESigner) ([]byte, error) {
	if signer.Certificate == nil || signer.PrivateKey == nil {
		return nil, errors.New("smime: signer.Certificate and signer.PrivateKey are required")
	}
	signedData, err := pkcs7.NewSignedData(mime)
	if err != nil {
		return nil, fmt.Errorf("smime: build signed-data: %w", err)
	}
	signedData.SetDigestAlgorithm(pkcs7.OIDDigestAlgorithmSHA256)
	if err := signedData.AddSigner(signer.Certificate, signer.PrivateKey, pkcs7.SignerInfoConfig{}); err != nil {
		return nil, fmt.Errorf("smime: add signer: %w", err)
	}
	for _, ca := range signer.Intermediates {
		signedData.AddCertificate(ca)
	}
	signedData.Detach()
	der, err := signedData.Finish()
	if err != nil {
		return nil, fmt.Errorf("smime: finalize signed-data: %w", err)
	}
	return assembleMultipartSigned(mime, der), nil
}

// EncryptMIME wraps mime in an S/MIME enveloped-data structure
// (application/pkcs7-mime; smime-type=enveloped-data). recipientCerts
// must include each recipient's public certificate; obtain them via
// ResolveRecipients with CertificateRetrieval=2.
//
// Default cipher: AES-128 CBC (the S/MIME 3 baseline). Callers needing
// AES-256 GCM should sign+encrypt manually with the pkcs7 library.
func EncryptMIME(mime []byte, recipientCerts []*x509.Certificate) ([]byte, error) {
	if len(recipientCerts) == 0 {
		return nil, errors.New("smime: at least one recipient certificate required")
	}
	der, err := pkcs7.Encrypt(mime, recipientCerts)
	if err != nil {
		return nil, fmt.Errorf("smime: encrypt: %w", err)
	}
	return assembleEnvelopedMIME(der), nil
}

// SignAndEncryptMIME signs mime then encrypts the signed envelope to
// recipientCerts. This is the form most MUAs expect for "signed and
// encrypted" delivery — the signature is verifiable only after decrypt.
func SignAndEncryptMIME(mime []byte, signer SMIMESigner, recipientCerts []*x509.Certificate) ([]byte, error) {
	signed, err := SignMIME(mime, signer)
	if err != nil {
		return nil, err
	}
	return EncryptMIME(signed, recipientCerts)
}

// assembleMultipartSigned wraps the original cleartext + the detached
// signature blob in a multipart/signed envelope.
func assembleMultipartSigned(mime, signatureDER []byte) []byte {
	boundary := "asmcp-smime-" + randomBoundarySuffix()
	var sb strings.Builder
	sb.WriteString("MIME-Version: 1.0\r\n")
	sb.WriteString("Content-Type: multipart/signed; protocol=\"application/pkcs7-signature\"; ")
	sb.WriteString("micalg=sha-256; boundary=\"")
	sb.WriteString(boundary)
	sb.WriteString("\"\r\n\r\n")
	sb.WriteString("--")
	sb.WriteString(boundary)
	sb.WriteString("\r\n")
	sb.Write(mime)
	if !endsWithCRLF(mime) {
		sb.WriteString("\r\n")
	}
	sb.WriteString("--")
	sb.WriteString(boundary)
	sb.WriteString("\r\n")
	sb.WriteString("Content-Type: application/pkcs7-signature; name=\"smime.p7s\"\r\n")
	sb.WriteString("Content-Transfer-Encoding: base64\r\n")
	sb.WriteString("Content-Disposition: attachment; filename=\"smime.p7s\"\r\n\r\n")
	sb.WriteString(base64Wrap(signatureDER))
	sb.WriteString("\r\n--")
	sb.WriteString(boundary)
	sb.WriteString("--\r\n")
	return []byte(sb.String())
}

// assembleEnvelopedMIME wraps a PKCS#7 enveloped-data DER blob in
// the standard application/pkcs7-mime envelope.
func assembleEnvelopedMIME(der []byte) []byte {
	var sb strings.Builder
	sb.WriteString("MIME-Version: 1.0\r\n")
	sb.WriteString("Content-Type: application/pkcs7-mime; smime-type=enveloped-data; name=\"smime.p7m\"\r\n")
	sb.WriteString("Content-Transfer-Encoding: base64\r\n")
	sb.WriteString("Content-Disposition: attachment; filename=\"smime.p7m\"\r\n\r\n")
	sb.WriteString(base64Wrap(der))
	sb.WriteString("\r\n")
	return []byte(sb.String())
}

func endsWithCRLF(b []byte) bool {
	n := len(b)
	return n >= 2 && b[n-2] == '\r' && b[n-1] == '\n'
}

// base64Wrap returns standard base64 encoded with 76-char line wraps,
// the format required by RFC 2045.
func base64Wrap(b []byte) string {
	enc := base64StdEncode(b)
	const width = 76
	var sb strings.Builder
	for i := 0; i < len(enc); i += width {
		end := min(i+width, len(enc))
		sb.WriteString(enc[i:end])
		if end < len(enc) {
			sb.WriteString("\r\n")
		}
	}
	return sb.String()
}

// base64StdEncode wraps encoding/base64.StdEncoding.EncodeToString.
func base64StdEncode(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func randomBoundarySuffix() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}
