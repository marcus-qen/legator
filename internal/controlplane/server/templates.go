package server

import (
	"fmt"
	"html/template"
	"strconv"
	"strings"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/auth"
	"github.com/marcus-qen/legator/internal/controlplane/fleet"
)

// Template data types for web UI.

// FleetSummary holds the status counts for the fleet overview.
type FleetSummary struct {
	Online            int
	Offline           int
	Degraded          int
	Total             int
	ReliabilityScore  int
	ReliabilityStatus string
}

// TemplateUser describes the logged-in user shown in page chrome.
type TemplateUser struct {
	Username    string
	Role        string
	Permissions map[auth.Permission]struct{}
}

// BasePage contains common layout metadata shared by all pages.
type BasePage struct {
	CurrentUser *TemplateUser
	Version     string
	ActiveNav   string
}

// FleetPageData is passed to the fleet.html template.
type FleetPageData struct {
	BasePage
	Probes  []*fleet.ProbeState
	Summary FleetSummary
	Commit  string
}

// FleetChatPageData is passed to fleet-chat.html template.
type FleetChatPageData struct {
	BasePage
	Inventory fleet.FleetInventory
}

// ProbePageData is passed to probe-detail.html and chat.html templates.
type ProbePageData struct {
	BasePage
	Probe  *fleet.ProbeState
	Uptime string
}

// AlertsPageData is passed to alerts.html template.
type AlertsPageData struct {
	BasePage
}

func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"statusClass":    templateStatusClass,
		"humanizeStatus": templateHumanizeStatus,
		"formatLastSeen": formatLastSeen,
		"humanBytes":     humanBytes,
		"hasPermission":  templateHasPermission,
	}
}

func templateHasPermission(user *TemplateUser, permission string) bool {
	if user == nil {
		return false
	}

	required := auth.Permission(strings.TrimSpace(permission))
	if required == "" {
		return false
	}

	if _, ok := user.Permissions[auth.PermAdmin]; ok {
		return true
	}

	_, ok := user.Permissions[required]
	return ok
}

func templateStatusClass(status string) string {
	switch strings.ToLower(status) {
	case "online":
		return "online"
	case "offline":
		return "offline"
	case "degraded":
		return "degraded"
	default:
		return "pending"
	}
}

func templateHumanizeStatus(status string) string {
	s := strings.ToLower(status)
	if s == "" {
		return "pending"
	}
	return s
}

func formatLastSeen(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format(time.RFC3339)
}

func humanBytes(v uint64) string {
	if v == 0 {
		return "0 B"
	}
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	value := float64(v)
	unit := 0
	for unit < len(units)-1 && value >= 1024 {
		value /= 1024
		unit++
	}
	if unit == 0 {
		return fmt.Sprintf("%.0f %s", value, units[unit])
	}
	return fmt.Sprintf("%.1f %s", value, units[unit])
}

func calculateUptime(start time.Time) string {
	if start.IsZero() {
		return "n/a"
	}
	secs := int64(time.Since(start).Seconds())
	if secs < 60 {
		return strconv.FormatInt(secs, 10) + "s"
	}
	mins := secs / 60
	secs %= 60
	hours := mins / 60
	mins %= 60
	days := hours / 24
	hours %= 24

	var parts []string
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%dd", days))
	}
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	if mins > 0 {
		parts = append(parts, fmt.Sprintf("%dm", mins))
	}
	if len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("%ds", secs))
	}
	return strings.Join(parts, " ")
}
