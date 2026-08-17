package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthzIsLivenessOnly(t *testing.T) {
	// `db` is nil here on purpose: if /healthz ever grows a database check, this
	// test breaks. Liveness must not depend on a downstream dependency.
	rec := httptest.NewRecorder()
	handleHealthz(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("healthz returned %d, want 200", rec.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("healthz body is not JSON: %v", err)
	}
	if body["version"] == "" {
		t.Error("version is empty; deploy verification depends on this field")
	}
}

func TestReadyzFailsBeforeDatabaseIsReady(t *testing.T) {
	dbReady.Store(false)
	t.Cleanup(func() { dbReady.Store(false) })

	rec := httptest.NewRecorder()
	handleReadyz(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz returned %d before the DB was ready, want 503", rec.Code)
	}
}

// LOW_STOCK_THRESHOLD is config category 4 -- promoted between environments --
// so the threshold has to actually change behaviour.
func TestIsLowStock(t *testing.T) {
	tests := []struct {
		name      string
		quantity  int
		threshold int
		want      bool
	}{
		{"well above", 100, 10, false},
		{"exactly at the threshold", 10, 10, false},
		{"one below", 9, 10, true},
		{"empty", 0, 10, true},
		{"same stock, stricter environment", 20, 25, true},
		{"same stock, looser environment", 20, 10, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isLowStock(tc.quantity, tc.threshold); got != tc.want {
				t.Errorf("isLowStock(%d, %d) = %v, want %v", tc.quantity, tc.threshold, got, tc.want)
			}
		})
	}
}

func TestEnvIntOrFallsBackOnGarbage(t *testing.T) {
	t.Setenv("TEST_THRESHOLD", "nonsense")
	if got := envIntOr("TEST_THRESHOLD", 10); got != 10 {
		t.Errorf("envIntOr with garbage = %d, want the fallback 10", got)
	}
	t.Setenv("TEST_THRESHOLD", "3")
	if got := envIntOr("TEST_THRESHOLD", 10); got != 3 {
		t.Errorf("envIntOr = %d, want 3", got)
	}
}

func TestLoadSettings(t *testing.T) {
	t.Setenv("PORT", "8081")
	t.Setenv("ENVIRONMENT", "prod")
	t.Setenv("LOG_LEVEL", "warn")
	t.Setenv("LOW_STOCK_THRESHOLD", "25")

	cfg := loadSettings()
	if cfg.port != "8081" || cfg.environment != "prod" || cfg.logLevel != "warn" || cfg.lowStock != 25 {
		t.Errorf("loadSettings did not read every setting: %+v", cfg)
	}
}
