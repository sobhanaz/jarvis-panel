package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"testing"
)

func TestPerfDefaultsAndLoadSave(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "perf-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	oldConfigDir := configDir
	configDir = tmpDir
	defer func() { configDir = oldConfigDir }()

	// 1. Missing file: default values must match requirements
	def := loadPerfConfig()
	if def.ProxyEncryption != false {
		t.Fatalf("expected proxy_encryption=false by default, got %v", def.ProxyEncryption)
	}
	if def.ProxyCompression != false {
		t.Fatalf("expected proxy_compression=false by default, got %v", def.ProxyCompression)
	}
	if def.ForceTLS != false {
		t.Fatalf("expected force_tls=false by default, got %v", def.ForceTLS)
	}
	if def.ChaffProfile != "off" {
		t.Fatalf("expected chaff_profile=off by default, got %s", def.ChaffProfile)
	}
	if def.DPIEnabled != false {
		t.Fatalf("expected dpi_enabled=false by default, got %v", def.DPIEnabled)
	}
	if def.DPIRate != "60/sec" {
		t.Fatalf("expected dpi_rate=60/sec by default, got %s", def.DPIRate)
	}
	if def.DPIBurst != 120 {
		t.Fatalf("expected dpi_burst=120 by default, got %d", def.DPIBurst)
	}

	// 2. Save custom config and verify file mode + values
	custom := perfConfig{
		ProxyEncryption:  true,
		ProxyCompression: true,
		ForceTLS:         false,
		ChaffProfile:     "mid",
		DPIEnabled:       false,
		DPIRate:          "500/min",
		DPIBurst:         150,
		FRPMaxPool:       80,
	}
	if err := savePerfConfig(custom); err != nil {
		t.Fatalf("savePerfConfig failed: %v", err)
	}

	// Check file permission 0600 (on POSIX systems)
	info, err := os.Stat(perfConfigPath())
	if err != nil {
		t.Fatalf("stat on perf.json failed: %v", err)
	}
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0600 {
			t.Fatalf("expected perf.json perm 0600, got %o", perm)
		}
	}

	// Reload and verify
	loaded := loadPerfConfig()
	if loaded != custom {
		t.Fatalf("expected %+v, got %+v", custom, loaded)
	}

	// FRPMaxPool <= 0 is normalized to the default pool of 50 when saving.
	zeroPool := custom
	zeroPool.FRPMaxPool = 0
	if err := savePerfConfig(zeroPool); err != nil {
		t.Fatalf("savePerfConfig failed: %v", err)
	}
	if got := loadPerfConfig().FRPMaxPool; got != 50 {
		t.Fatalf("expected FRPMaxPool 0 to normalize to 50, got %d", got)
	}
	if err := savePerfConfig(custom); err != nil {
		t.Fatalf("savePerfConfig failed: %v", err)
	}

	// 3. Partial config unmarshaling: missing fields keep defaults
	partialJSON := `{"proxy_encryption": true}`
	if err := os.WriteFile(perfConfigPath(), []byte(partialJSON), 0600); err != nil {
		t.Fatal(err)
	}
	partial := loadPerfConfig()
	if !partial.ProxyEncryption {
		t.Fatalf("expected proxy_encryption=true")
	}
	if partial.ForceTLS {
		t.Fatalf("expected force_tls to retain default false when omitted")
	}
	if partial.ChaffProfile != "off" {
		t.Fatalf("expected chaff_profile to retain default off when omitted")
	}
	if partial.DPIEnabled {
		t.Fatalf("expected dpi_enabled to retain default false when omitted")
	}
	if partial.DPIRate != "60/sec" || partial.DPIBurst != 120 {
		t.Fatalf("expected default rate and burst, got %s, %d", partial.DPIRate, partial.DPIBurst)
	}
}

func TestPerfEnvOverrides(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "perf-env-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	oldConfigDir := configDir
	configDir = tmpDir
	defer func() { configDir = oldConfigDir }()

	// File with defaults: enc=false, comp=false, tls=true
	cfg := defaultPerfConfig()
	if err := savePerfConfig(cfg); err != nil {
		t.Fatal(err)
	}

	// Set env overrides
	t.Setenv("PERF_ENC", "1")
	t.Setenv("PERF_COMP", "1")
	t.Setenv("PERF_TLS", "0")

	eff, over := effectivePerfConfig()
	if !eff.ProxyEncryption || !over["proxy_encryption"] {
		t.Fatalf("PERF_ENC override failed")
	}
	if !eff.ProxyCompression || !over["proxy_compression"] {
		t.Fatalf("PERF_COMP override failed")
	}
	if eff.ForceTLS || !over["force_tls"] {
		t.Fatalf("PERF_TLS override failed")
	}
}

func TestPerfAPIEndpoints(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "perf-api-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	oldConfigDir := configDir
	configDir = tmpDir
	defer func() { configDir = oldConfigDir }()

	// 1. GET /api/perf
	req := httptest.NewRequest("GET", "/api/perf", nil)
	rr := httptest.NewRecorder()
	handlePerfGet(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/perf returned %d: %s", rr.Code, rr.Body.String())
	}
	var resp perfStatusResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.ForceTLS != false || resp.ChaffProfile != "off" || resp.DPIBurst != 120 {
		t.Fatalf("unexpected defaults in GET response: %+v", resp)
	}

	// 2. POST /api/perf action=update
	tTrue := true
	tFalse := false
	burstVal := 120
	updateBody := perfPostRequest{
		Action:           "update",
		ProxyEncryption:  &tTrue,
		ProxyCompression: &tFalse,
		ForceTLS:         &tFalse,
		DPIBurst:         &burstVal,
	}
	bData, _ := json.Marshal(updateBody)
	req = httptest.NewRequest("POST", "/api/perf", bytes.NewReader(bData))
	rr = httptest.NewRecorder()
	handlePerfPost(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("POST update returned %d: %s", rr.Code, rr.Body.String())
	}

	updated := loadPerfConfig()
	if !updated.ProxyEncryption || updated.ProxyCompression || updated.ForceTLS || updated.DPIBurst != 120 {
		t.Fatalf("update was not saved properly: %+v", updated)
	}

	// 3. POST /api/perf invalid action
	req = httptest.NewRequest("POST", "/api/perf", bytes.NewReader([]byte(`{"action":"invalid-action"}`)))
	rr = httptest.NewRecorder()
	handlePerfPost(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid action, got %d", rr.Code)
	}
}
