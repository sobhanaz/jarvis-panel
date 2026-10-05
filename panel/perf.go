package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type perfConfig struct {
	ProxyEncryption  bool   `json:"proxy_encryption"`
	ProxyCompression bool   `json:"proxy_compression"`
	ForceTLS         bool   `json:"force_tls"`
	ChaffProfile     string `json:"chaff_profile"`
	DPIEnabled       bool   `json:"dpi_enabled"`
	DPIRate          string `json:"dpi_rate"`
	DPIBurst         int    `json:"dpi_burst"`
	FRPMaxPool       int    `json:"frp_max_pool"`
	AutoTune         bool   `json:"auto_tune"`
}

type perfStatusResponse struct {
	ProxyEncryption  bool            `json:"proxy_encryption"`
	ProxyCompression bool            `json:"proxy_compression"`
	ForceTLS         bool            `json:"force_tls"`
	ChaffProfile     string          `json:"chaff_profile"`
	DPIEnabled       bool            `json:"dpi_enabled"`
	DPIRate          string          `json:"dpi_rate"`
	DPIBurst         int             `json:"dpi_burst"`
	FRPMaxPool       int             `json:"frp_max_pool"`
	AutoTune         bool            `json:"auto_tune"`
	InSync           bool            `json:"in_sync"`
	SyncDetails      string          `json:"sync_details"`
	Role             string          `json:"role"`
	DPIActive        bool            `json:"dpi_active"`
	ChaffActive      bool            `json:"chaff_active"`
	Overridden       map[string]bool `json:"overridden,omitempty"`
}

type perfPostRequest struct {
	Action           string  `json:"action"`
	ProxyEncryption  *bool   `json:"proxy_encryption,omitempty"`
	ProxyCompression *bool   `json:"proxy_compression,omitempty"`
	ForceTLS         *bool   `json:"force_tls,omitempty"`
	ChaffProfile     *string `json:"chaff_profile,omitempty"`
	DPIEnabled       *bool   `json:"dpi_enabled,omitempty"`
	DPIRate          *string `json:"dpi_rate,omitempty"`
	DPIBurst         *int    `json:"dpi_burst,omitempty"`
	FRPMaxPool       *int    `json:"frp_max_pool,omitempty"`
	AutoTune         *bool   `json:"auto_tune,omitempty"`
}

func perfConfigPath() string {
	return filepath.Join(configDir, "perf.json")
}

func defaultPerfConfig() perfConfig {
	return perfConfig{
		ProxyEncryption:  false,
		ProxyCompression: false,
		ForceTLS:         false,
		ChaffProfile:     "off",
		DPIEnabled:       false,
		DPIRate:          "60/sec",
		DPIBurst:         120,
		FRPMaxPool:       50,
		AutoTune:         false,
	}
}

func loadPerfConfig() perfConfig {
	def := defaultPerfConfig()
	data, err := os.ReadFile(perfConfigPath())
	if err != nil {
		return def
	}
	var raw struct {
		ProxyEncryption  *bool   `json:"proxy_encryption"`
		ProxyCompression *bool   `json:"proxy_compression"`
		ForceTLS         *bool   `json:"force_tls"`
		ChaffProfile     *string `json:"chaff_profile"`
		DPIEnabled       *bool   `json:"dpi_enabled"`
		DPIRate          *string `json:"dpi_rate"`
		DPIBurst         *int    `json:"dpi_burst"`
		FRPMaxPool       *int    `json:"frp_max_pool"`
		AutoTune         *bool   `json:"auto_tune"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return def
	}
	c := def
	if raw.ProxyEncryption != nil {
		c.ProxyEncryption = *raw.ProxyEncryption
	}
	if raw.ProxyCompression != nil {
		c.ProxyCompression = *raw.ProxyCompression
	}
	if raw.ForceTLS != nil {
		c.ForceTLS = *raw.ForceTLS
	}
	if raw.ChaffProfile != nil {
		p := strings.ToLower(strings.TrimSpace(*raw.ChaffProfile))
		if p == "off" || p == "low" || p == "mid" {
			c.ChaffProfile = p
		}
	}
	if raw.DPIEnabled != nil {
		c.DPIEnabled = *raw.DPIEnabled
	}
	if raw.DPIRate != nil && strings.TrimSpace(*raw.DPIRate) != "" {
		c.DPIRate = strings.TrimSpace(*raw.DPIRate)
	}
	if raw.DPIBurst != nil && *raw.DPIBurst > 0 {
		c.DPIBurst = *raw.DPIBurst
	}
	if raw.FRPMaxPool != nil && *raw.FRPMaxPool >= 10 {
		c.FRPMaxPool = *raw.FRPMaxPool
	}
	if raw.AutoTune != nil {
		c.AutoTune = *raw.AutoTune
	}
	return c
}

func savePerfConfig(c perfConfig) error {
	_ = os.MkdirAll(configDir, 0700)
	if c.ChaffProfile == "" {
		c.ChaffProfile = "off"
	}
	if c.DPIRate == "" {
		c.DPIRate = "300/min"
	}
	if c.DPIBurst <= 0 {
		c.DPIBurst = 100
	}
	if c.FRPMaxPool <= 0 {
		c.FRPMaxPool = 50
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(perfConfigPath(), append(data, '\n'), 0600)
}

func effectivePerfConfig() (perfConfig, map[string]bool) {
	c := loadPerfConfig()
	overridden := map[string]bool{}
	if v := os.Getenv("PERF_ENC"); v != "" {
		c.ProxyEncryption = (v == "1" || strings.ToLower(v) == "true")
		overridden["proxy_encryption"] = true
	}
	if v := os.Getenv("PERF_COMP"); v != "" {
		c.ProxyCompression = (v == "1" || strings.ToLower(v) == "true")
		overridden["proxy_compression"] = true
	}
	if v := os.Getenv("PERF_TLS"); v != "" {
		c.ForceTLS = (v == "1" || strings.ToLower(v) == "true")
		overridden["force_tls"] = true
	}
	return c, overridden
}

func checkLiveTomlSync(c perfConfig) (bool, string, string) {
	role := "none"
	if _, err := os.Stat("/etc/frp/frpc.toml"); err == nil {
		role = "foreign"
	} else if _, err := os.Stat("/etc/frp/frps.toml"); err == nil {
		role = "iran"
	}

	switch role {
	case "foreign":
		data, err := os.ReadFile("/etc/frp/frpc.toml")
		if err != nil {
			return false, "cannot read /etc/frp/frpc.toml", role
		}
		content := string(data)
		hasTLSByte := strings.Contains(content, "transport.tls.disableCustomTLSFirstByte = true") || strings.Contains(content, "transport.tls.disableCustomTLSFirstByte=true")
		hasEnc := strings.Contains(content, "transport.useEncryption = true") || strings.Contains(content, "transport.useEncryption=true")
		hasComp := strings.Contains(content, "transport.useCompression = true") || strings.Contains(content, "transport.useCompression=true")

		if c.ForceTLS != hasTLSByte {
			return false, fmt.Sprintf("TLS setting (%v) does not match frpc.toml (%v)", c.ForceTLS, hasTLSByte), role
		}
		if c.ProxyEncryption != hasEnc {
			return false, fmt.Sprintf("Proxy encryption (%v) does not match frpc.toml (%v)", c.ProxyEncryption, hasEnc), role
		}
		if c.ProxyCompression != hasComp {
			return false, fmt.Sprintf("Proxy compression (%v) does not match frpc.toml (%v)", c.ProxyCompression, hasComp), role
		}
		return true, "All settings match live frpc.toml", role
	case "iran":
		data, err := os.ReadFile("/etc/frp/frps.toml")
		if err != nil {
			return false, "cannot read /etc/frp/frps.toml", role
		}
		content := string(data)
		hasTLSForce := strings.Contains(content, "transport.tls.force = true") || strings.Contains(content, "transport.tls.force=true")
		if c.ForceTLS != hasTLSForce {
			return false, fmt.Sprintf("Force TLS setting (%v) does not match frps.toml (%v)", c.ForceTLS, hasTLSForce), role
		}
		return true, "All settings match live frps.toml", role
	default:
		return true, "No tunnel configured yet", role
	}
}

func runPerfCmd(args ...string) (string, error) {
	script, err := greScriptPath()
	if err != nil {
		return "", err
	}
	cmd := exec.Command("bash", append([]string{script}, args...)...)
	cmd.Env = append(os.Environ(), "GRE_SKIP_PANEL=1", "TERM=dumb")
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	runErr := cmd.Run()
	return strings.TrimSpace(buf.String()), runErr
}

func handlePerfGet(w http.ResponseWriter, r *http.Request) {
	c, over := effectivePerfConfig()
	inSync, details, role := checkLiveTomlSync(c)

	dpiActive := false
	if out, err := exec.Command("iptables", "-L", "HASHEM-DPI", "-n").CombinedOutput(); err == nil {
		dpiActive = strings.Contains(string(out), "HASHEM-DPI")
	}

	chaffActive := false
	if out, err := exec.Command("systemctl", "is-active", "gre-chaff").CombinedOutput(); err == nil && strings.TrimSpace(string(out)) == "active" {
		chaffActive = true
	} else if out, err := exec.Command("systemctl", "list-units", "--type=service", "--state=running").CombinedOutput(); err == nil && strings.Contains(string(out), "gre-chaff") {
		chaffActive = true
	}

	resp := perfStatusResponse{
		ProxyEncryption:  c.ProxyEncryption,
		ProxyCompression: c.ProxyCompression,
		ForceTLS:         c.ForceTLS,
		ChaffProfile:     c.ChaffProfile,
		DPIEnabled:       c.DPIEnabled,
		DPIRate:          c.DPIRate,
		DPIBurst:         c.DPIBurst,
		InSync:           inSync,
		SyncDetails:      details,
		Role:             role,
		DPIActive:        dpiActive,
		ChaffActive:      chaffActive,
		Overridden:       over,
	}
	writeJSON(w, resp)
}

func handlePerfPost(w http.ResponseWriter, r *http.Request) {
	var body perfPostRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIError(w, r, "E-PERF-01", "invalid request body")
		return
	}

	c := loadPerfConfig()

	switch body.Action {
	case "update":
		if body.ProxyEncryption != nil {
			c.ProxyEncryption = *body.ProxyEncryption
		}
		if body.ProxyCompression != nil {
			c.ProxyCompression = *body.ProxyCompression
		}
		if body.ForceTLS != nil {
			c.ForceTLS = *body.ForceTLS
		}
		if body.ChaffProfile != nil {
			p := strings.ToLower(strings.TrimSpace(*body.ChaffProfile))
			if p == "off" || p == "low" || p == "mid" {
				c.ChaffProfile = p
			}
		}
		if body.DPIEnabled != nil {
			c.DPIEnabled = *body.DPIEnabled
		}
		if body.DPIRate != nil && strings.TrimSpace(*body.DPIRate) != "" {
			c.DPIRate = strings.TrimSpace(*body.DPIRate)
		}
		if body.DPIBurst != nil && *body.DPIBurst > 0 {
			c.DPIBurst = *body.DPIBurst
		}
		if err := savePerfConfig(c); err != nil {
			writeAPIError(w, r, "E-PERF-02", "failed to save config: "+err.Error())
			return
		}
		recordError("E-PERF-00", "perf", "Performance settings updated")
		writeJSON(w, map[string]string{"status": "ok", "detail": "Settings updated in perf.json"})

	case "apply":
		if body.ProxyEncryption != nil {
			c.ProxyEncryption = *body.ProxyEncryption
		}
		if body.ProxyCompression != nil {
			c.ProxyCompression = *body.ProxyCompression
		}
		if body.ForceTLS != nil {
			c.ForceTLS = *body.ForceTLS
		}
		if body.ChaffProfile != nil {
			p := strings.ToLower(strings.TrimSpace(*body.ChaffProfile))
			if p == "off" || p == "low" || p == "mid" {
				c.ChaffProfile = p
			}
		}
		if body.DPIEnabled != nil {
			c.DPIEnabled = *body.DPIEnabled
		}
		if body.DPIRate != nil && strings.TrimSpace(*body.DPIRate) != "" {
			c.DPIRate = strings.TrimSpace(*body.DPIRate)
		}
		if body.DPIBurst != nil && *body.DPIBurst > 0 {
			c.DPIBurst = *body.DPIBurst
		}
		if err := savePerfConfig(c); err != nil {
			writeAPIError(w, r, "E-PERF-02", "failed to save config: "+err.Error())
			return
		}

		out, err := runPerfCmd("perf", "apply")
		if err != nil {
			recordError("E-PERF-03", "perf", "Apply failed: "+out)
			writeAPIError(w, r, "E-PERF-03", out)
			return
		}
		recordError("E-PERF-00", "perf", "Performance settings applied and tunnels restarted")
		writeJSON(w, map[string]string{"status": "ok", "detail": out})

	case "set-chaff":
		if body.ChaffProfile == nil {
			writeAPIError(w, r, "E-PERF-01", "chaff_profile required")
			return
		}
		p := strings.ToLower(strings.TrimSpace(*body.ChaffProfile))
		if p != "off" && p != "low" && p != "mid" {
			writeAPIError(w, r, "E-PERF-01", "invalid chaff_profile: must be off, low, or mid")
			return
		}
		c.ChaffProfile = p
		if err := savePerfConfig(c); err != nil {
			writeAPIError(w, r, "E-PERF-02", "failed to save config: "+err.Error())
			return
		}
		out, err := runPerfCmd("perf", "chaff", p)
		if err != nil {
			writeAPIError(w, r, "E-PERF-03", out)
			return
		}
		recordError("E-PERF-00", "perf", "Chaff profile set to "+p)
		writeJSON(w, map[string]string{"status": "ok", "detail": out})

	case "set-dpi":
		if body.DPIEnabled == nil {
			writeAPIError(w, r, "E-PERF-01", "dpi_enabled required")
			return
		}
		c.DPIEnabled = *body.DPIEnabled
		if err := savePerfConfig(c); err != nil {
			writeAPIError(w, r, "E-PERF-02", "failed to save config: "+err.Error())
			return
		}
		action := "off"
		if c.DPIEnabled {
			action = "on"
		}
		out, err := runPerfCmd("perf", "dpi", action)
		if err != nil {
			writeAPIError(w, r, "E-PERF-03", out)
			return
		}
		recordError("E-PERF-00", "perf", "DPI shield set to "+action)
		writeJSON(w, map[string]string{"status": "ok", "detail": out})

	case "reset":
		c = defaultPerfConfig()
		if err := savePerfConfig(c); err != nil {
			writeAPIError(w, r, "E-PERF-02", "failed to save config: "+err.Error())
			return
		}
		out, err := runPerfCmd("perf", "reset")
		if err != nil {
			recordError("E-PERF-03", "perf", "Reset failed: "+out)
			writeAPIError(w, r, "E-PERF-03", out)
			return
		}
		recordError("E-PERF-00", "perf", "Performance settings reset to safe defaults")
		writeJSON(w, map[string]string{"status": "ok", "detail": "Settings reset to safe wire-speed defaults."})

	default:
		writeAPIError(w, r, "E-PERF-01", "unknown action: "+body.Action)
	}
}
