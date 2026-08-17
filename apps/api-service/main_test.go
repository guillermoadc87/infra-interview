package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthzIsLivenessOnly(t *testing.T) {
	// The point of this test is the ABSENCE of a database. `db` is nil here, so
	// if /healthz ever grows a database check this test panics or fails -- which
	// is exactly the regression we want to catch. Liveness must never depend on
	// a downstream dependency.
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	handleHealthz(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("healthz returned %d, want 200", rec.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("healthz body is not JSON: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("status = %q, want %q", body["status"], "ok")
	}
	if body["version"] == "" {
		t.Error("version is empty; the deploy verification depends on this field")
	}
}

func TestReadyzFailsBeforeDatabaseIsReady(t *testing.T) {
	dbReady.Store(false)
	t.Cleanup(func() { dbReady.Store(false) })

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()

	handleReadyz(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz returned %d before the DB was ready, want 503", rec.Code)
	}
	var body map[string]string
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if body["db"] != "connecting" {
		t.Errorf("db = %q, want %q", body["db"], "connecting")
	}
}

func TestValidateOrder(t *testing.T) {
	const limit = 100

	tests := []struct {
		name    string
		req     CreateOrderRequest
		wantErr bool
	}{
		{"valid", CreateOrderRequest{ProductID: 1, Quantity: 2, CustomerID: "c-1"}, false},
		{"at the limit", CreateOrderRequest{ProductID: 1, Quantity: limit, CustomerID: "c-1"}, false},
		{"over the limit", CreateOrderRequest{ProductID: 1, Quantity: limit + 1, CustomerID: "c-1"}, true},
		{"zero quantity", CreateOrderRequest{ProductID: 1, Quantity: 0, CustomerID: "c-1"}, true},
		{"negative quantity", CreateOrderRequest{ProductID: 1, Quantity: -5, CustomerID: "c-1"}, true},
		{"missing product", CreateOrderRequest{Quantity: 1, CustomerID: "c-1"}, true},
		{"missing customer", CreateOrderRequest{ProductID: 1, Quantity: 1}, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateOrder(tc.req, limit)
			if tc.wantErr && err == nil {
				t.Errorf("validateOrder(%+v) = nil, want an error", tc.req)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("validateOrder(%+v) = %v, want nil", tc.req, err)
			}
		})
	}
}

// FEATURE_ORDER_LIMIT is config category 4 -- a business setting that IS
// promoted between environments. Prod deliberately runs a lower limit than
// staging until a promotion moves it, so the limit must actually be enforced
// per-environment rather than being decorative.
func TestOrderLimitIsEnforcedPerEnvironment(t *testing.T) {
	req := CreateOrderRequest{ProductID: 1, Quantity: 50, CustomerID: "c-1"}

	if err := validateOrder(req, 100); err != nil {
		t.Errorf("quantity 50 with a limit of 100 should be accepted, got %v", err)
	}
	if err := validateOrder(req, 25); err == nil {
		t.Error("quantity 50 with a limit of 25 should be rejected, got nil")
	}
}

func TestEnvIntOrFallsBackOnGarbage(t *testing.T) {
	t.Setenv("TEST_LIMIT", "not-a-number")
	if got := envIntOr("TEST_LIMIT", 42); got != 42 {
		t.Errorf("envIntOr with garbage = %d, want the fallback 42", got)
	}

	t.Setenv("TEST_LIMIT", "-1")
	if got := envIntOr("TEST_LIMIT", 42); got != 42 {
		t.Errorf("envIntOr with a negative value = %d, want the fallback 42", got)
	}

	t.Setenv("TEST_LIMIT", "7")
	if got := envIntOr("TEST_LIMIT", 42); got != 7 {
		t.Errorf("envIntOr = %d, want 7", got)
	}
}

func TestLoadSettingsReadsEachCategoryFromItsOwnVariable(t *testing.T) {
	t.Setenv("PORT", "9999")
	t.Setenv("ENVIRONMENT", "staging")
	t.Setenv("PAYMENTS_URL", "https://payments.example.com")
	t.Setenv("LOG_LEVEL", "info")
	t.Setenv("FEATURE_ORDER_LIMIT", "25")

	cfg := loadSettings()

	if cfg.port != "9999" || cfg.environment != "staging" ||
		cfg.paymentsURL != "https://payments.example.com" ||
		cfg.logLevel != "info" || cfg.featureOrderLim != 25 {
		t.Errorf("loadSettings did not read every setting: %+v", cfg)
	}
}
