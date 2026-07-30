package main

import (
	"context"
	"testing"
)

func TestIMAPSessionCountersCloseExactlyOnce(t *testing.T) {
	d := newIMAPD(nil)
	d.sessionOpened()
	_, cancel := context.WithCancel(context.Background())
	sess := &imapSession{d: d, cancel: cancel, counted: true}

	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}

	stats := d.connectionStats()
	if stats.Active != 0 || stats.Total != 1 || stats.HighWater != 1 {
		t.Fatalf("connection stats = %+v, want active=0 total=1 highWater=1", stats)
	}
}
