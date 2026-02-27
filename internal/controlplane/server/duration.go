package server

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// parseHumanDuration parses Go durations plus day suffixes (e.g. 30d, 90d).
func parseHumanDuration(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, fmt.Errorf("duration required")
	}

	if strings.HasSuffix(raw, "d") {
		daysPart := strings.TrimSuffix(raw, "d")
		days, err := strconv.ParseFloat(daysPart, 64)
		if err != nil || days < 0 {
			return 0, fmt.Errorf("invalid day duration")
		}
		return time.Duration(days * float64(24*time.Hour)), nil
	}

	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, err
	}
	if d < 0 {
		return 0, fmt.Errorf("duration must be >= 0")
	}
	return d, nil
}
