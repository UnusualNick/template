package config

import (
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	setValidEnvironment(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Fatalf("HTTPAddr = %q", cfg.HTTPAddr)
	}
	if cfg.DatabaseMaxConns != 10 || cfg.DatabaseMinConns != 2 {
		t.Fatalf("pool size = %d/%d", cfg.DatabaseMinConns, cfg.DatabaseMaxConns)
	}
	if cfg.DatabaseQueryTimeout != 3*time.Second {
		t.Fatalf("DatabaseQueryTimeout = %s", cfg.DatabaseQueryTimeout)
	}
	if cfg.PositionCheckInterval != 30*time.Second || cfg.PositionStaleAfter != time.Minute || cfg.PositionWorkers != 4 || cfg.PositionQueueSize != 100 {
		t.Fatalf("position settings = %#v", cfg)
	}
	if cfg.PushHTTPURL != "http://localhost:8090" || cfg.PushRequestTimeout != time.Second {
		t.Fatalf("push settings = %#v", cfg)
	}
}

func TestLoadRejectsMissingRequiredValue(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("DATABASE_URL", "")

	if _, err := Load(); err == nil {
		t.Fatal("Load() succeeded with empty DATABASE_URL")
	}
}

func TestLoadRejectsInvalidPoolRange(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("DATABASE_MIN_CONNS", "11")

	if _, err := Load(); err == nil {
		t.Fatal("Load() succeeded with min conns greater than max conns")
	}
}

func TestLoadRejectsInvalidPositionSettings(t *testing.T) {
	for _, tc := range []struct {
		name, value string
	}{
		{"POSITION_CHECK_INTERVAL", "0s"},
		{"POSITION_STALE_AFTER", "never"},
		{"POSITION_WORKERS", "0"},
		{"POSITION_QUEUE_SIZE", "-1"},
		{"PUSH_HTTP_URL", ""},
		{"PUSH_REQUEST_TIMEOUT", "0s"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setValidEnvironment(t)
			t.Setenv(tc.name, tc.value)
			if _, err := Load(); err == nil {
				t.Fatalf("Load() succeeded with %s=%q", tc.name, tc.value)
			}
		})
	}
}

func setValidEnvironment(t *testing.T) {
	t.Helper()
	values := map[string]string{
		"HTTP_ADDR":                  ":8080",
		"HTTP_READ_TIMEOUT":          "10s",
		"HTTP_READ_HEADER_TIMEOUT":   "5s",
		"HTTP_WRITE_TIMEOUT":         "15s",
		"HTTP_IDLE_TIMEOUT":          "60s",
		"LOG_LEVEL":                  "info",
		"SHUTDOWN_TIMEOUT":           "10s",
		"DATABASE_URL":               "postgres://localhost/tripgo",
		"DATABASE_MAX_CONNS":         "10",
		"DATABASE_MIN_CONNS":         "2",
		"DATABASE_MAX_CONN_LIFETIME": "30m",
		"DATABASE_CONNECT_TIMEOUT":   "5s",
		"DATABASE_QUERY_TIMEOUT":     "3s",
		"POSITION_CHECK_INTERVAL":    "30s",
		"POSITION_STALE_AFTER":       "60s",
		"POSITION_WORKERS":           "4",
		"POSITION_QUEUE_SIZE":        "100",
		"PUSH_HTTP_URL":              "http://localhost:8090",
		"PUSH_REQUEST_TIMEOUT":       "1s",
	}
	for name, value := range values {
		t.Setenv(name, value)
	}
}
