package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	ansiReset  = "\x1b[0m"
	ansiGreen  = "\x1b[32m"
	ansiRed    = "\x1b[31m"
	ansiYellow = "\x1b[33m"
)

func RenderTable(out io.Writer, headers []string, rows [][]string) {
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len(h)
	}

	for _, row := range rows {
		for i, cell := range row {
			if i >= len(widths) {
				continue
			}
			if l := visibleLen(cell); l > widths[i] {
				widths[i] = l
			}
		}
	}

	writeRow(out, headers, widths)
	writeDivider(out, widths)
	for _, row := range rows {
		writeRow(out, row, widths)
	}
}

func writeDivider(out io.Writer, widths []int) {
	for i, w := range widths {
		if i > 0 {
			fmt.Fprint(out, "  ")
		}
		fmt.Fprint(out, strings.Repeat("-", w))
	}
	fmt.Fprintln(out)
}

func writeRow(out io.Writer, cols []string, widths []int) {
	for i, w := range widths {
		val := ""
		if i < len(cols) {
			val = cols[i]
		}
		fmt.Fprint(out, padRight(val, w))
		if i < len(widths)-1 {
			fmt.Fprint(out, "  ")
		}
	}
	fmt.Fprintln(out)
}

func padRight(v string, width int) string {
	if width <= 0 {
		return v
	}
	pad := width - visibleLen(v)
	if pad <= 0 {
		return v
	}
	return v + strings.Repeat(" ", pad)
}

func visibleLen(s string) int {
	inEscape := false
	count := 0
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if inEscape {
			if ch == 'm' {
				inEscape = false
			}
			continue
		}
		if ch == 27 {
			inEscape = true
			continue
		}
		count++
	}
	return count
}

func ColorStatus(status string) string {
	switch strings.ToLower(status) {
	case "online":
		return ansiGreen + status + ansiReset
	case "offline":
		return ansiRed + status + ansiReset
	case "degraded":
		return ansiYellow + status + ansiReset
	default:
		return status
	}
}

func PrintJSON(out io.Writer, v any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func Truncate(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(s) <= max {
		return s
	}
	if max == 1 {
		return s[:max]
	}
	return s[:max-1] + "…"
}

func FormatTimeOrDash(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Format("2006-01-02 15:04:05")
}
