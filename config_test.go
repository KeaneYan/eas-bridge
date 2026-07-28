package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeTestConfig(t *testing.T, values map[string]any) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(configDir(), 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath(), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadConfigCalendarPollDefaultsToMailPoll(t *testing.T) {
	writeTestConfig(t, map[string]any{
		"server":       "https://mail.example.com/Microsoft-Server-ActiveSync",
		"user":         "user@example.com",
		"password":     "secret",
		"poll_seconds": 90,
	})

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PollSecs != 90 || cfg.CalendarPollSecs != 90 {
		t.Fatalf("poll intervals = mail %d, calendar %d; want 90/90", cfg.PollSecs, cfg.CalendarPollSecs)
	}
}

func TestLoadConfigCalendarPollCanBeConfiguredIndependently(t *testing.T) {
	writeTestConfig(t, map[string]any{
		"server":                "https://mail.example.com/Microsoft-Server-ActiveSync",
		"user":                  "user@example.com",
		"password":              "secret",
		"poll_seconds":          300,
		"calendar_poll_seconds": 60,
	})

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PollSecs != 300 || cfg.CalendarPollSecs != 60 {
		t.Fatalf("poll intervals = mail %d, calendar %d; want 300/60", cfg.PollSecs, cfg.CalendarPollSecs)
	}
}

func TestInitConfigWritesCompleteDefaults(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := initConfig(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(configDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.CalDAVAddr != "127.0.0.1:8008" || cfg.PollSecs != 60 || cfg.CalendarPollSecs != 60 {
		t.Fatalf("incomplete defaults: %+v", cfg)
	}
}
