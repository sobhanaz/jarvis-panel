package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type watchdogConfig struct {
	Enabled          bool   `json:"enabled"`
	IntervalSec      int    `json:"interval_sec"`
	FailThreshold    int    `json:"fail_threshold"`
	TGBotToken       string `json:"tg_bot_token"`
	TGChatID         string `json:"tg_chat_id"`
	TGRoute          string `json:"tg_route"`
	TGTunnelPort     int    `json:"tg_tunnel_port"`
	BackupEveryHours int    `json:"backup_every_hours"`
	BackupDailyAt    string `json:"backup_daily_at"`
	LastCheck        string `json:"last_check"`
	ConsecFails      int    `json:"consec_fails"`
	LastAlert        string `json:"last_alert"`
	DownSince        int64  `json:"down_since,omitempty"`
	LastBackup       int64  `json:"last_backup,omitempty"`
	LastBackupDate   string `json:"last_backup_date,omitempty"`
}

// backupDir is where `hashem backup` writes encrypted archives; a var so tests
// can point it at a temp dir instead of the real system path.
var backupDir = "/var/backups/hashem"

type backupItem struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	Time string `json:"time"`
}

type watchdogStatusResponse struct {
	Enabled          bool         `json:"enabled"`
	Status           string       `json:"status"`
	CheckResult      string       `json:"check_result"`
	LastCheck        string       `json:"last_check"`
	ConsecFails      int          `json:"consec_fails"`
	FailThreshold    int          `json:"fail_threshold"`
	TGBotTokenMasked string       `json:"tg_bot_token_masked"`
	TGChatID         string       `json:"tg_chat_id"`
	TGRoute          string       `json:"tg_route"`
	TGTunnelPort     int          `json:"tg_tunnel_port"`
	ScheduleMode     string       `json:"schedule_mode"`
	BackupEveryHours int          `json:"backup_every_hours"`
	BackupDailyAt    string       `json:"backup_daily_at"`
	ScheduleHuman    string       `json:"schedule_human"`
	Backups          []backupItem `json:"backups"`
	TunnelPorts      []int        `json:"tunnel_ports"`
	RecentEvents     []errEvent   `json:"recent_events"`
}

type watchdogPostRequest struct {
	Action     string `json:"action"`
	BotToken   string `json:"bot_token,omitempty"`
	ChatID     string `json:"chat_id,omitempty"`
	Route      string `json:"route,omitempty"`
	TunnelPort int    `json:"tunnel_port,omitempty"`
	Mode       string `json:"mode,omitempty"`
	Hours      int    `json:"hours,omitempty"`
	At         string `json:"at,omitempty"`
	File       string `json:"file,omitempty"`
}

func watchdogConfigPath() string {
	return filepath.Join(configDir, "watchdog.json")
}

func loadWatchdogConfig() watchdogConfig {
	def := watchdogConfig{
		Enabled:       true,
		IntervalSec:   60,
		FailThreshold: 2,
		TGRoute:       "direct",
	}
	data, err := os.ReadFile(watchdogConfigPath())
	if err != nil {
		return def
	}
	var c watchdogConfig
	if err := json.Unmarshal(data, &c); err != nil {
		return def
	}
	if c.IntervalSec <= 0 {
		c.IntervalSec = 60
	}
	if c.FailThreshold <= 0 {
		c.FailThreshold = 2
	}
	if c.TGRoute == "" {
		c.TGRoute = "direct"
	}
	return c
}

func saveWatchdogConfig(c watchdogConfig) error {
	_ = os.MkdirAll(configDir, 0700)
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(watchdogConfigPath(), append(data, '\n'), 0600)
}

func maskBotToken(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	parts := strings.SplitN(token, ":", 2)
	if len(parts) == 2 && len(parts[1]) > 6 {
		p2 := parts[1]
		return parts[0] + ":" + p2[:3] + "..." + p2[len(p2)-3:]
	}
	if len(token) > 10 {
		return token[:4] + "..." + token[len(token)-4:]
	}
	return "******"
}

func formatSchedule(c watchdogConfig) string {
	if c.BackupEveryHours > 0 {
		return fmt.Sprintf("Every %d hours", c.BackupEveryHours)
	}
	if c.BackupDailyAt != "" {
		return fmt.Sprintf("Daily at %s", c.BackupDailyAt)
	}
	return "Disabled"
}

func listBackups() []backupItem {
	dir := backupDir
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []backupItem{}
	}
	var items []backupItem
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, "hashem-backup-") && strings.HasSuffix(name, ".enc") {
			info, err := e.Info()
			if err != nil {
				continue
			}
			items = append(items, backupItem{
				Name: name,
				Size: info.Size(),
				Time: info.ModTime().Format("2006-01-02 15:04:05"),
			})
		}
	}
	// Sort newest first
	sort.Slice(items, func(i, j int) bool {
		return items[i].Name > items[j].Name
	})
	return items
}

func collectTunnelPorts() []int {
	ports := localStatus().ProxyPorts
	for _, p := range loadPeers() {
		ports = append(ports, p.Ports...)
	}
	return uniqInts(ports)
}

func collectWatchdogEvents() []errEvent {
	var events []errEvent
	f, err := os.Open(errorLogPath())
	if err == nil {
		defer f.Close()
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			var ev errEvent
			if json.Unmarshal(scanner.Bytes(), &ev) == nil {
				if ev.Endpoint == "watchdog" || strings.HasPrefix(ev.Code, "E-WD-") || strings.Contains(strings.ToLower(ev.Detail), "watchdog") {
					events = append(events, ev)
				}
			}
		}
		if scanErr := scanner.Err(); scanErr != nil {
			_ = scanErr
		}
	}
	if len(events) == 0 {
		errMu.Lock()
		for _, ev := range errEvents {
			if ev.Endpoint == "watchdog" || strings.HasPrefix(ev.Code, "E-WD-") || strings.Contains(strings.ToLower(ev.Detail), "watchdog") {
				events = append(events, ev)
			}
		}
		errMu.Unlock()
	}
	if len(events) > 10 {
		events = events[len(events)-10:]
	}
	// Reverse to show newest first
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}
	return events
}

func runWatchdogCmd(args ...string) (string, error) {
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

func handleWatchdogGet(w http.ResponseWriter, r *http.Request) {
	c := loadWatchdogConfig()
	checkResult, _ := runWatchdogCmd("watchdog", "check")

	status := "disabled"
	if c.Enabled {
		if strings.Contains(checkResult, "status=up") {
			status = "up"
		} else {
			status = "down"
		}
	}

	mode := "off"
	if c.BackupEveryHours > 0 {
		mode = "interval"
	} else if c.BackupDailyAt != "" {
		mode = "daily"
	}

	resp := watchdogStatusResponse{
		Enabled:          c.Enabled,
		Status:           status,
		CheckResult:      checkResult,
		LastCheck:        c.LastCheck,
		ConsecFails:      c.ConsecFails,
		FailThreshold:    c.FailThreshold,
		TGBotTokenMasked: maskBotToken(c.TGBotToken),
		TGChatID:         c.TGChatID,
		TGRoute:          c.TGRoute,
		TGTunnelPort:     c.TGTunnelPort,
		ScheduleMode:     mode,
		BackupEveryHours: c.BackupEveryHours,
		BackupDailyAt:    c.BackupDailyAt,
		ScheduleHuman:    formatSchedule(c),
		Backups:          listBackups(),
		TunnelPorts:      collectTunnelPorts(),
		RecentEvents:     collectWatchdogEvents(),
	}

	writeJSON(w, resp)
}

func isValidTimeHHMM(s string) bool {
	if len(s) != 5 || s[2] != ':' {
		return false
	}
	h, err1 := strconv.Atoi(s[:2])
	m, err2 := strconv.Atoi(s[3:])
	return err1 == nil && err2 == nil && h >= 0 && h <= 23 && m >= 0 && m <= 59
}

func handleWatchdogPost(w http.ResponseWriter, r *http.Request) {
	var body watchdogPostRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIError(w, r, "E-WD-01", "invalid request body")
		return
	}

	switch body.Action {
	case "on":
		out, err := runWatchdogCmd("watchdog", "on")
		if err != nil {
			writeAPIError(w, r, "E-ACTION-03", out)
			return
		}
		c := loadWatchdogConfig()
		c.Enabled = true
		_ = saveWatchdogConfig(c)
		recordError("E-WD-00", "watchdog", "Watchdog enabled")
		writeJSON(w, map[string]string{"status": "ok", "detail": "Watchdog enabled"})

	case "off":
		out, err := runWatchdogCmd("watchdog", "off")
		if err != nil {
			writeAPIError(w, r, "E-ACTION-03", out)
			return
		}
		c := loadWatchdogConfig()
		c.Enabled = false
		_ = saveWatchdogConfig(c)
		recordError("E-WD-00", "watchdog", "Watchdog disabled")
		writeJSON(w, map[string]string{"status": "ok", "detail": "Watchdog disabled"})

	case "test":
		out, err := runWatchdogCmd("watchdog", "test")
		if err != nil {
			code := "E-WD-02"
			lower := strings.ToLower(out)
			if strings.Contains(lower, "socks") || strings.Contains(lower, "route") || strings.Contains(lower, "tunnel") {
				code = "E-WD-05"
			}
			recordError(code, "watchdog", "Telegram test failed: "+out)
			writeAPIError(w, r, code, out)
			return
		}
		recordError("E-WD-00", "watchdog", "Telegram test message sent successfully")
		writeJSON(w, map[string]string{"status": "ok", "detail": out})

	case "backup-now":
		out, err := runWatchdogCmd("backup", "now")
		if err != nil {
			recordError("E-WD-03", "watchdog", "Backup failed: "+out)
			writeAPIError(w, r, "E-WD-03", out)
			return
		}
		recordError("E-WD-00", "watchdog", "Backup created successfully")
		writeJSON(w, map[string]string{"status": "ok", "detail": out})

	case "restore":
		base := filepath.Base(body.File)
		if body.File == "" || base != body.File || !strings.HasSuffix(base, ".enc") || strings.Contains(base, "..") {
			writeAPIError(w, r, "E-WD-01", "invalid backup file name")
			return
		}
		path := filepath.Join(backupDir, base)
		if _, err := os.Stat(path); err != nil {
			writeAPIError(w, r, "E-WD-01", "backup file not found")
			return
		}
		out, err := runWatchdogCmd("backup", "restore", path)
		if err != nil {
			recordError("E-WD-04", "watchdog", "Restore failed: "+out)
			writeAPIError(w, r, "E-WD-04", out)
			return
		}
		recordError("E-WD-00", "watchdog", "Backup restored: "+base)
		writeJSON(w, map[string]string{"status": "ok", "detail": out})

	case "set-telegram":
		c := loadWatchdogConfig()
		if body.BotToken != "" && !strings.Contains(body.BotToken, "...") {
			c.TGBotToken = strings.TrimSpace(body.BotToken)
		}
		c.TGChatID = strings.TrimSpace(body.ChatID)
		if body.Route == "direct" || body.Route == "tunnel" {
			c.TGRoute = body.Route
		}
		c.TGTunnelPort = body.TunnelPort
		if err := saveWatchdogConfig(c); err != nil {
			writeAPIError(w, r, "E-WD-01", "failed to save config: "+err.Error())
			return
		}
		recordError("E-WD-00", "watchdog", "Telegram settings updated")
		writeJSON(w, map[string]string{"status": "ok", "detail": "Telegram settings saved"})

	case "set-schedule":
		c := loadWatchdogConfig()
		switch body.Mode {
		case "interval":
			if body.Hours < 1 || body.Hours > 168 {
				writeAPIError(w, r, "E-WD-01", "interval hours must be between 1 and 168")
				return
			}
			c.BackupEveryHours = body.Hours
			c.BackupDailyAt = ""
		case "daily":
			if !isValidTimeHHMM(body.At) {
				writeAPIError(w, r, "E-WD-01", "daily time must be in HH:MM format (24h)")
				return
			}
			c.BackupEveryHours = 0
			c.BackupDailyAt = body.At
		case "off":
			c.BackupEveryHours = 0
			c.BackupDailyAt = ""
		default:
			writeAPIError(w, r, "E-WD-01", "unknown schedule mode: "+body.Mode)
			return
		}
		if err := saveWatchdogConfig(c); err != nil {
			writeAPIError(w, r, "E-WD-01", "failed to save schedule: "+err.Error())
			return
		}
		recordError("E-WD-00", "watchdog", "Backup schedule updated: "+formatSchedule(c))
		writeJSON(w, map[string]string{"status": "ok", "detail": "Backup schedule saved"})

	default:
		writeAPIError(w, r, "E-WD-01", "unknown action: "+body.Action)
	}
}

func handleBackupDownload(w http.ResponseWriter, r *http.Request) {
	f := r.URL.Query().Get("f")
	base := filepath.Base(f)
	if f == "" || base != f || !strings.HasSuffix(base, ".enc") || strings.Contains(base, "..") {
		writeAPIError(w, r, "E-WD-01", "invalid backup file name")
		return
	}
	path := filepath.Join(backupDir, base)
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		writeAPIError(w, r, "E-WD-01", "backup file not found")
		return
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", base))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(st.Size(), 10))
	http.ServeFile(w, r, path)
}
