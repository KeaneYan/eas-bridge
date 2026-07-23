// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"context"
	"net/http"
	"testing"

	"github.com/hstern/go-activesync/wbxml"
)

func TestValidateCert_perCertStatus(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageValidateCert, "ValidateCert",
				wbxml.E(wbxml.PageValidateCert, "Status", wbxml.Text("1")),
				wbxml.E(wbxml.PageValidateCert, "Certificate",
					wbxml.E(wbxml.PageValidateCert, "Status", wbxml.Text("1")),
				),
				wbxml.E(wbxml.PageValidateCert, "Certificate",
					wbxml.E(wbxml.PageValidateCert, "Status", wbxml.Text("17")), // unspecified
				),
			),
		}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	results, err := c.ValidateCert(context.Background(), [][]byte{{0x30, 0x01}, {0x30, 0x02}}, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Status != 1 || results[1].Status != 17 {
		t.Errorf("results = %+v", results)
	}
}

func TestValidateCert_validation(t *testing.T) {
	c, _, _ := newTestClient(t, nil)
	if _, err := c.ValidateCert(context.Background(), nil, nil, false); err == nil {
		t.Error("want error")
	}
}

func TestValidateCert_emitsCertificateChain(t *testing.T) {
	c, cap, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	})
	chain := [][]byte{{0xCA, 0x01}, {0xCA, 0x02}}
	_, _ = c.ValidateCert(context.Background(), [][]byte{{0x30, 0x01}}, chain, false)
	req, _ := wbxml.Unmarshal(cap.body, wbxml.DefaultRegistry())
	chainEl := req.Root.Find("CertificateChain")
	if chainEl == nil {
		t.Fatal("CertificateChain missing")
	}
	if len(chainEl.Children) != 2 {
		t.Errorf("chain children = %d, want 2", len(chainEl.Children))
	}
}

func TestValidateCert_postErrorPropagates(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	})
	if _, err := c.ValidateCert(context.Background(), [][]byte{{0x30}}, nil, false); err == nil {
		t.Error("want HTTP error")
	}
}

func TestValidateCert_emptyResponseRejected(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	})
	if _, err := c.ValidateCert(context.Background(), [][]byte{{0x30}}, nil, false); err == nil {
		t.Error("want error on empty response")
	}
}

func TestValidateCert_topLevelStatusError(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageValidateCert, "ValidateCert",
				wbxml.E(wbxml.PageValidateCert, "Status", wbxml.Text("4")),
			),
		}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	if _, err := c.ValidateCert(context.Background(), [][]byte{{0x30}}, nil, false); !IsStatusCode(err, 4) {
		t.Errorf("err = %v", err)
	}
}
