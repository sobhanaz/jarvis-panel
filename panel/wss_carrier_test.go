package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestWSSConfigDefaultsAndLoadSave(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "hashem_wss_test_*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	oldConfigDir := configDir
	configDir = tmpDir
	defer func() { configDir = oldConfigDir }()

	cfg := defaultWSSConfig()
	if cfg.ListenPort != 8443 {
		t.Errorf("expected default listen port 8443, got %d", cfg.ListenPort)
	}
	if cfg.LocalBridgeUDP != 19998 {
		t.Errorf("expected default local bridge UDP 19998, got %d", cfg.LocalBridgeUDP)
	}

	cfg.ListenPort = 9443
	cfg.SNI = "cdn.example.com"
	if err := saveWSSConfig(cfg); err != nil {
		t.Fatalf("saveWSSConfig failed: %v", err)
	}

	loaded := loadWSSConfig()
	if loaded.ListenPort != 9443 {
		t.Errorf("expected loaded port 9443, got %d", loaded.ListenPort)
	}
	if loaded.SNI != "cdn.example.com" {
		t.Errorf("expected SNI 'cdn.example.com', got '%s'", loaded.SNI)
	}
}

func TestSelfSignedCertGeneration(t *testing.T) {
	cert, err := generateSelfSignedCert("speedtest.net")
	if err != nil {
		t.Fatalf("generateSelfSignedCert failed: %v", err)
	}
	if len(cert.Certificate) == 0 {
		t.Errorf("expected at least one certificate in chain")
	}
	if cert.PrivateKey == nil {
		t.Errorf("expected private key to be generated")
	}
}

func TestWSSCarrierStartStop(t *testing.T) {
	cfg := defaultWSSConfig()
	cfg.Enabled = true

	if err := startWSSCarrier(cfg); err != nil {
		t.Fatalf("startWSSCarrier failed: %v", err)
	}

	st := getWSSStatus()
	if !st.Running {
		t.Errorf("expected WSS carrier to be reported as running")
	}

	if err := stopWSSCarrier(); err != nil {
		t.Fatalf("stopWSSCarrier failed: %v", err)
	}

	st = getWSSStatus()
	if st.Running {
		t.Errorf("expected WSS carrier to be stopped")
	}
}

func TestCarrierWSSIntegration(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "hashem_carrier_wss_test_*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	oldConfigDir := configDir
	configDir = tmpDir
	defer func() { configDir = oldConfigDir }()

	cfg := defaultCarrierConfig()
	foundWSS := false
	for _, c := range cfg.Candidates {
		if c == "wss:8443" {
			foundWSS = true
			break
		}
	}
	if !foundWSS {
		t.Errorf("expected 'wss:8443' in default candidates, got: %v", cfg.Candidates)
	}

	// Test POST /api/carrier with action: "set_mode", mode: "wss:8443"
	body, _ := json.Marshal(carrierPostRequest{
		Action: "set_mode",
		Mode:   "wss:8443",
	})
	req := httptest.NewRequest("POST", "/api/carrier", bytes.NewReader(body))
	w := httptest.NewRecorder()
	handleCarrierPost(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify status includes WSS
	reqGet := httptest.NewRequest("GET", "/api/carrier", nil)
	wGet := httptest.NewRecorder()
	handleCarrierGet(wGet, reqGet)

	var resp carrierStatusResponse
	if err := json.Unmarshal(wGet.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode carrier status: %v", err)
	}
	if resp.Active != "wss:8443" {
		t.Errorf("expected active carrier 'wss:8443', got '%s'", resp.Active)
	}
}

// Deterministic reproduction of the leaked-listener bug: if stop() has already run by
// the time runServer() gets going (the goroutine start races the stop), runServer()
// must notice and return. Previously it registered its HTTP server afterwards and
// blocked in ListenAndServe forever, keeping the port bound with nothing to stop it.
func TestWSSRunServerAfterStopReturnsWithoutListening(t *testing.T) {
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := free.Addr().(*net.TCPAddr).Port
	free.Close()

	cfg := defaultWSSConfig()
	cfg.Role = "server"
	cfg.UseTLS = false
	cfg.ListenPort = port
	cfg.LocalBridgeUDP = 0

	ctx, cancel := context.WithCancel(context.Background())
	m := &wssCarrierManager{cfg: cfg, ctx: ctx, cancel: cancel}
	_ = m.stop() // stop wins the race

	done := make(chan struct{})
	go func() {
		m.runServer()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runServer kept running after stop() (leaked listener)")
	}

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("port %d still bound after stop: %v", port, err)
	}
	ln.Close()
}

// Real-timing smoke test (not deterministic on its own): rapid start/stop cycles must
// leave the port free. TestWSSRunServerAfterStopReturnsWithoutListening is the
// deterministic regression test for the same bug.
func TestWSSCarrierRapidStartStopLeavesNoListener(t *testing.T) {
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := free.Addr().(*net.TCPAddr).Port
	free.Close()

	cfg := defaultWSSConfig()
	cfg.Enabled = true
	cfg.Role = "server"
	cfg.UseTLS = false
	cfg.ListenPort = port
	cfg.LocalBridgeUDP = 0

	for i := 0; i < 100; i++ {
		if err := startWSSCarrier(cfg); err != nil {
			t.Fatalf("start #%d failed: %v", i, err)
		}
		if err := stopWSSCarrier(); err != nil {
			t.Fatalf("stop #%d failed: %v", i, err)
		}
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			ln.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("port %d still bound after stop: %v", port, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
