// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/hstern/go-activesync/wbxml"
)

func TestResolveRecipients_basic(t *testing.T) {
	c, cap, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageResolveRecipients, "ResolveRecipients",
				wbxml.E(wbxml.PageResolveRecipients, "Status", wbxml.Text("1")),
				wbxml.E(wbxml.PageResolveRecipients, "Response",
					wbxml.E(wbxml.PageResolveRecipients, "To", wbxml.Text("alice")),
					wbxml.E(wbxml.PageResolveRecipients, "Status", wbxml.Text("1")),
					wbxml.E(wbxml.PageResolveRecipients, "RecipientCount", wbxml.Text("1")),
					wbxml.E(wbxml.PageResolveRecipients, "Recipient",
						wbxml.E(wbxml.PageResolveRecipients, "Type", wbxml.Text("1")),
						wbxml.E(wbxml.PageResolveRecipients, "DisplayName", wbxml.Text("Alice Engineer")),
						wbxml.E(wbxml.PageResolveRecipients, "EmailAddress", wbxml.Text("alice@x")),
						wbxml.E(wbxml.PageResolveRecipients, "Availability",
							wbxml.E(wbxml.PageResolveRecipients, "Status", wbxml.Text("1")),
							wbxml.E(wbxml.PageResolveRecipients, "MergedFreeBusy", wbxml.Text("000022000000")),
						),
					),
				),
			),
		}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	now := time.Date(2026, 5, 9, 9, 0, 0, 0, time.UTC)
	res, err := c.ResolveRecipients(context.Background(), []string{"alice"}, ResolveOptions{
		AvailabilityStart: now,
		AvailabilityEnd:   now.Add(8 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || len(res[0].Recipients) != 1 {
		t.Fatalf("res = %+v", res)
	}
	r := res[0].Recipients[0]
	if r.EmailAddress != "alice@x" {
		t.Errorf("email = %q", r.EmailAddress)
	}
	if r.MergedFreeBusy != "000022000000" {
		t.Errorf("free/busy = %q", r.MergedFreeBusy)
	}
	// Request shape: To + Availability options.
	req, _ := wbxml.Unmarshal(cap.body, wbxml.DefaultRegistry())
	if to := req.Root.Find("To"); to == nil || to.TextContent() != "alice" {
		t.Errorf("To = %v", to)
	}
	if av := req.Root.Find("Availability"); av == nil {
		t.Error("Availability not in request")
	}
}

func TestResolveRecipients_validation(t *testing.T) {
	c, _, _ := newTestClient(t, nil)
	if _, err := c.ResolveRecipients(context.Background(), nil, ResolveOptions{}); err == nil {
		t.Error("want error")
	}
	if _, err := c.ResolveRecipients(context.Background(), []string{}, ResolveOptions{}); err == nil {
		t.Error("want error for empty slice")
	}
}

func TestResolveRecipients_emitsAllOptions(t *testing.T) {
	c, cap, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{Root: wbxml.E(wbxml.PageResolveRecipients, "ResolveRecipients",
			wbxml.E(wbxml.PageResolveRecipients, "Status", wbxml.Text("1")),
		)}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	now := time.Date(2026, 5, 9, 9, 0, 0, 0, time.UTC)
	if _, err := c.ResolveRecipients(context.Background(),
		[]string{"alice", "bob@x"}, ResolveOptions{
			CertificateRetrieval:   2,
			MaxCertificates:        5,
			MaxAmbiguousRecipients: 10,
			AvailabilityStart:      now,
			AvailabilityEnd:        now.Add(time.Hour),
			PictureMaxBytes:        102400,
		}); err != nil {
		t.Fatal(err)
	}
	req, _ := wbxml.Unmarshal(cap.body, wbxml.DefaultRegistry())
	tos := req.Root.FindAll("To")
	if len(tos) != 2 || tos[0].TextContent() != "alice" || tos[1].TextContent() != "bob@x" {
		t.Errorf("To list = %v", tos)
	}
	wantText := map[string]string{
		"CertificateRetrieval":   "2",
		"MaxCertificates":        "5",
		"MaxAmbiguousRecipients": "10",
	}
	for name, want := range wantText {
		if el := req.Root.Find(name); el == nil || el.TextContent() != want {
			t.Errorf("%s = %v, want %q", name, el, want)
		}
	}
	if av := req.Root.Find("Availability"); av == nil {
		t.Error("Availability missing")
	}
	pic := req.Root.Find("Picture")
	if pic == nil {
		t.Fatal("Picture options missing")
	}
	if max := pic.Find("MaxSize"); max == nil || max.TextContent() != "102400" {
		t.Errorf("Picture/MaxSize = %v", max)
	}
}

func TestResolveRecipients_omitsAvailabilityWithoutBothEnds(t *testing.T) {
	c, cap, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{Root: wbxml.E(wbxml.PageResolveRecipients, "ResolveRecipients",
			wbxml.E(wbxml.PageResolveRecipients, "Status", wbxml.Text("1")),
		)}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	// Only Start set — no End → Availability subtree must be omitted.
	if _, err := c.ResolveRecipients(context.Background(), []string{"alice"}, ResolveOptions{
		AvailabilityStart: time.Date(2026, 5, 9, 9, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	req, _ := wbxml.Unmarshal(cap.body, wbxml.DefaultRegistry())
	if av := req.Root.Find("Availability"); av != nil {
		t.Errorf("Availability emitted with only one of Start/End: %v", av)
	}
}

func TestResolveRecipients_parsesCertificatesAndPicture(t *testing.T) {
	cert1 := []byte{0x30, 0x82, 0xCE, 0x11}
	cert2 := []byte{0x30, 0x82, 0xCE, 0x22}
	picJPEG := []byte{0xff, 0xd8, 0xff, 0xe0}
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{Root: wbxml.E(wbxml.PageResolveRecipients, "ResolveRecipients",
			wbxml.E(wbxml.PageResolveRecipients, "Status", wbxml.Text("1")),
			wbxml.E(wbxml.PageResolveRecipients, "Response",
				wbxml.E(wbxml.PageResolveRecipients, "To", wbxml.Text("alice@x")),
				wbxml.E(wbxml.PageResolveRecipients, "Status", wbxml.Text("1")),
				wbxml.E(wbxml.PageResolveRecipients, "Recipient",
					wbxml.E(wbxml.PageResolveRecipients, "Type", wbxml.Text("1")),
					wbxml.E(wbxml.PageResolveRecipients, "EmailAddress", wbxml.Text("alice@x")),
					wbxml.E(wbxml.PageResolveRecipients, "Certificates",
						wbxml.E(wbxml.PageResolveRecipients, "Certificate", wbxml.Opaque(cert1)),
						wbxml.E(wbxml.PageResolveRecipients, "Certificate", wbxml.Opaque(cert2)),
					),
					wbxml.E(wbxml.PageResolveRecipients, "Picture", wbxml.Opaque(picJPEG)),
				),
			),
		)}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	res, err := c.ResolveRecipients(context.Background(), []string{"alice"}, ResolveOptions{
		CertificateRetrieval: 2,
		PictureMaxBytes:      8192,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || len(res[0].Recipients) != 1 {
		t.Fatalf("got %+v", res)
	}
	r := res[0].Recipients[0]
	if len(r.Certificates) != 2 ||
		!bytesEqual(r.Certificates[0], cert1) ||
		!bytesEqual(r.Certificates[1], cert2) {
		t.Errorf("Certificates = %x", r.Certificates)
	}
	if !bytesEqual(r.Picture, picJPEG) {
		t.Errorf("Picture = %x", r.Picture)
	}
	if r.Type != 1 {
		t.Errorf("Type = %d", r.Type)
	}
}

func TestResolveRecipients_topLevelStatusError(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{Root: wbxml.E(wbxml.PageResolveRecipients, "ResolveRecipients",
			wbxml.E(wbxml.PageResolveRecipients, "Status", wbxml.Text("110")),
		)}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	_, err := c.ResolveRecipients(context.Background(), []string{"alice"}, ResolveOptions{})
	if err == nil {
		t.Fatal("want StatusError")
	}
	if !IsStatusCode(err, 110) {
		t.Errorf("err = %v", err)
	}
}

func TestResolveRecipients_emptyResponseIsError(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		// empty body → nil resp
	})
	if _, err := c.ResolveRecipients(context.Background(), []string{"alice"}, ResolveOptions{}); err == nil {
		t.Error("want error for empty response")
	}
}

func TestResolveRecipients_perResponseStatusKept(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{Root: wbxml.E(wbxml.PageResolveRecipients, "ResolveRecipients",
			wbxml.E(wbxml.PageResolveRecipients, "Status", wbxml.Text("1")),
			wbxml.E(wbxml.PageResolveRecipients, "Response",
				wbxml.E(wbxml.PageResolveRecipients, "To", wbxml.Text("alice@nowhere")),
				wbxml.E(wbxml.PageResolveRecipients, "Status", wbxml.Text("4")), // ProtocolError
			),
		)}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	res, err := c.ResolveRecipients(context.Background(), []string{"alice@nowhere"}, ResolveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Status != 4 {
		t.Errorf("res = %+v; per-response Status should be exposed, not error", res)
	}
}

func TestResolveRecipients_postErrorPropagates(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	if _, err := c.ResolveRecipients(context.Background(), []string{"alice"}, ResolveOptions{}); err == nil {
		t.Fatal("want HTTP error")
	}
}

func TestResolveRecipients_skipsNonCertificateChildren(t *testing.T) {
	// Spec only allows <Certificate> inside <Certificates>; a stray child
	// must be ignored without polluting the parsed cert list.
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{Root: wbxml.E(wbxml.PageResolveRecipients, "ResolveRecipients",
			wbxml.E(wbxml.PageResolveRecipients, "Status", wbxml.Text("1")),
			wbxml.E(wbxml.PageResolveRecipients, "Response",
				wbxml.E(wbxml.PageResolveRecipients, "To", wbxml.Text("alice")),
				wbxml.E(wbxml.PageResolveRecipients, "Status", wbxml.Text("1")),
				wbxml.E(wbxml.PageResolveRecipients, "Recipient",
					wbxml.E(wbxml.PageResolveRecipients, "Type", wbxml.Text("1")),
					wbxml.E(wbxml.PageResolveRecipients, "Certificates",
						wbxml.E(wbxml.PageResolveRecipients, "Status", wbxml.Text("1")), // not a Certificate — must skip
						wbxml.E(wbxml.PageResolveRecipients, "Certificate", wbxml.Opaque([]byte("REAL-CERT"))),
					),
				),
			),
		)}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	res, err := c.ResolveRecipients(context.Background(), []string{"alice"}, ResolveOptions{CertificateRetrieval: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || len(res[0].Recipients) != 1 || len(res[0].Recipients[0].Certificates) != 1 {
		t.Fatalf("got %+v", res)
	}
	if string(res[0].Recipients[0].Certificates[0]) != "REAL-CERT" {
		t.Errorf("cert = %q", res[0].Recipients[0].Certificates[0])
	}
}

// bytesEqual is a tiny local helper so this file doesn't need to import
// "bytes" just for the certificate / picture comparisons above.
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
