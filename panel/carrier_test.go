package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
)

func TestCarrierDefaultsAndLoadSave(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "carrier-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	oldConfigDir := configDir
	configDir = tmpDir
	defer func() { configDir = oldConfigDir }()

	// Default when file is missing
	def := loadCarrierConfig()
	if def.Mode != "direct" {
		t.Fatalf("expected mode=direct (auto failover was removed), got %s", def.Mode)
	}
	if def.ActiveCarrier != "direct" {
		t.Fatalf("expected active_carrier=direct, got %s", def.ActiveCarrier)
	}
	if def.FOUPort1 != 443 || def.FOUPort2 != 55555 {
		t.Fatalf("expected fou_port1=443, fou_port2=55555, got %d, %d", def.FOUPort1, def.FOUPort2)
	}
	if want := []string{"direct", "wss:8443"}; !reflect.DeepEqual(def.Candidates, want) {
		t.Fatalf("expected candidates %v, got %v", want, def.Candidates)
	}

	// Save and reload
	def.ActiveCarrier = "wss:8443"
	def.SwitchCount = 1
	if err := saveCarrierConfig(def); err != nil {
		t.Fatal(err)
	}

	loaded := loadCarrierConfig()
	if loaded.ActiveCarrier != "wss:8443" {
		t.Fatalf("expected active_carrier=wss:8443, got %s", loaded.ActiveCarrier)
	}
	if loaded.SwitchCount != 1 {
		t.Fatalf("expected switch_count=1, got %d", loaded.SwitchCount)
	}
}

func TestCarrierAPIEndpoints(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "carrier-api-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	oldConfigDir := configDir
	configDir = tmpDir
	defer func() { configDir = oldConfigDir }()

	// Initial GET
	req := httptest.NewRequest("GET", "/api/carrier", nil)
	w := httptest.NewRecorder()
	handleCarrierGet(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp carrierStatusResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Mode != "direct" || resp.ActiveCarrier != "direct" || resp.Active != "direct" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if len(resp.FouPorts) != 2 || resp.FouPorts[0] != 443 || resp.FouPorts[1] != 55555 {
		t.Fatalf("unexpected fou_ports: %+v", resp.FouPorts)
	}

	// cycle_next switches to the WSS carrier, which starts a real listener; stop it afterwards.
	defer func() { _ = stopWSSCarrier() }()

	// POST cycle_next
	postBody, _ := json.Marshal(carrierPostRequest{Action: "cycle_next"})
	req = httptest.NewRequest("POST", "/api/carrier", bytes.NewReader(postBody))
	w = httptest.NewRecorder()
	handleCarrierPost(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on cycle_next, got %d", w.Code)
	}

	var postResp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &postResp); err != nil {
		t.Fatal(err)
	}
	if postResp["status"] != "ok" {
		t.Fatalf("expected status=ok, got %+v", postResp)
	}

	// Verify carrier changed
	cfg := loadCarrierConfig()
	if cfg.ActiveCarrier != "wss:8443" {
		t.Fatalf("expected active_carrier=wss:8443 after cycle from direct, got %s", cfg.ActiveCarrier)
	}

	// POST set-ports
	portsBody, _ := json.Marshal(carrierPostRequest{Action: "set-ports", FouPorts: []int{8443, 60000}})
	req = httptest.NewRequest("POST", "/api/carrier", bytes.NewReader(portsBody))
	w = httptest.NewRecorder()
	handleCarrierPost(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on set-ports, got %d", w.Code)
	}
	cfg = loadCarrierConfig()
	if cfg.FOUPort1 != 8443 || cfg.FOUPort2 != 60000 {
		t.Fatalf("expected ports 8443, 60000, got %d, %d", cfg.FOUPort1, cfg.FOUPort2)
	}
}

// Servers installed before FOU/auto-failover were removed must keep working after
// an update: their carrier.json is migrated to direct and persisted once.
func TestCarrierLegacyConfigMigration(t *testing.T) {
	tmpDir := t.TempDir()
	oldConfigDir := configDir
	configDir = tmpDir
	defer func() { configDir = oldConfigDir }()

	legacy := `{"mode":"auto","active_carrier":"fou:443","fou_port1":443,"fou_port2":55555,` +
		`"candidates":["direct","fou:443","fou:55555","wss:8443"],"switch_count":58}`
	if err := os.WriteFile(carrierConfigPath(), []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}

	c := loadCarrierConfig()
	if c.Mode != "direct" || c.ActiveCarrier != "direct" {
		t.Fatalf("expected mode/active migrated to direct, got mode=%s active=%s", c.Mode, c.ActiveCarrier)
	}
	if want := []string{"direct", "wss:8443"}; !reflect.DeepEqual(c.Candidates, want) {
		t.Fatalf("expected FOU candidates stripped -> %v, got %v", want, c.Candidates)
	}
	if c.SwitchCount != 58 {
		t.Fatalf("migration must not reset switch_count, got %d", c.SwitchCount)
	}

	// Persisted: a second load reads the migrated file and changes nothing.
	raw, err := os.ReadFile(carrierConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	var onDisk carrierConfig
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatal(err)
	}
	if onDisk.Mode != "direct" || onDisk.ActiveCarrier != "direct" {
		t.Fatalf("migration was not persisted: %+v", onDisk)
	}
}
