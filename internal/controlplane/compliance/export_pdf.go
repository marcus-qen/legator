package compliance

import (
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/jung-kurt/gofpdf"
)

const (
	pdfMargin    = 15.0
	pdfPageW     = 210.0 // A4
	pdfPageH     = 297.0 // A4
	pdfBodyW     = pdfPageW - 2*pdfMargin
	pdfLineH     = 6.0
	pdfSmallLine = 5.0
)

// colourForStatus maps a compliance status to an RGB colour (r,g,b 0-255).
func colourForStatus(status string) (int, int, int) {
	switch status {
	case StatusPass:
		return 34, 139, 34 // forest green
	case StatusFail:
		return 178, 34, 34 // firebrick
	case StatusWarning:
		return 218, 165, 32 // goldenrod
	case StatusSkipped:
		return 128, 128, 128 // grey
	default:
		return 100, 100, 100 // dark grey
	}
}

// WritePDF generates a compliance report PDF and writes it to w.
// fleetScope describes the scope of the report (e.g. a tag or "all probes").
func WritePDF(store *Store, filter ExportFilter, fleetScope string, w io.Writer) error {
	results, err := fetchForExport(store, filter)
	if err != nil {
		return fmt.Errorf("fetch results for pdf: %w", err)
	}

	summary, err := store.Summary()
	if err != nil {
		return fmt.Errorf("compute summary for pdf: %w", err)
	}

	pdf := gofpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(pdfMargin, pdfMargin, pdfMargin)
	pdf.SetAutoPageBreak(true, pdfMargin)
	pdf.AddPage()

	// ── Header ──────────────────────────────────────────────────────────────
	pdf.SetFont("Helvetica", "B", 20)
	pdf.SetTextColor(30, 30, 80)
	pdf.CellFormat(pdfBodyW, 12, "Compliance Report", "", 1, "C", false, 0, "")

	pdf.SetFont("Helvetica", "", 10)
	pdf.SetTextColor(80, 80, 80)
	pdf.CellFormat(pdfBodyW, 6, "Generated: "+time.Now().UTC().Format("2006-01-02 15:04:05 UTC"), "", 1, "C", false, 0, "")
	pdf.CellFormat(pdfBodyW, 6, "Fleet scope: "+fleetScope, "", 1, "C", false, 0, "")
	pdf.CellFormat(pdfBodyW, 6, "Filter: "+buildExportFilterDescription(filter), "", 1, "C", false, 0, "")
	pdf.Ln(4)

	// Horizontal rule
	pdf.SetDrawColor(180, 180, 200)
	pdf.Line(pdfMargin, pdf.GetY(), pdfMargin+pdfBodyW, pdf.GetY())
	pdf.Ln(4)

	// ── Executive Summary ───────────────────────────────────────────────────
	pdf.SetFont("Helvetica", "B", 14)
	pdf.SetTextColor(30, 30, 80)
	pdf.CellFormat(pdfBodyW, 9, "Executive Summary", "", 1, "L", false, 0, "")
	pdf.Ln(1)

	// Score box
	scoreColor := 34
	if summary.ScorePct < 60 {
		scoreColor = 178
	} else if summary.ScorePct < 80 {
		scoreColor = 218
	}
	pdf.SetFont("Helvetica", "B", 36)
	pdf.SetTextColor(scoreColor, scoreColor*2/3, 34)
	pdf.CellFormat(pdfBodyW, 16, fmt.Sprintf("%.1f%%", summary.ScorePct), "", 1, "C", false, 0, "")

	pdf.SetFont("Helvetica", "", 10)
	pdf.SetTextColor(60, 60, 60)
	pdf.CellFormat(pdfBodyW, 6, "Overall Compliance Score", "", 1, "C", false, 0, "")
	pdf.Ln(4)

	// Summary stats table
	summaryData := [][]string{
		{"Total Checks", fmt.Sprintf("%d", summary.TotalChecks)},
		{"Total Probes", fmt.Sprintf("%d", summary.TotalProbes)},
		{"Passing", fmt.Sprintf("%d", summary.Passing)},
		{"Failing", fmt.Sprintf("%d", summary.Failing)},
		{"Warning", fmt.Sprintf("%d", summary.Warning)},
		{"Unknown", fmt.Sprintf("%d", summary.Unknown)},
	}
	colW1 := pdfBodyW * 0.6
	colW2 := pdfBodyW * 0.4

	pdf.SetFont("Helvetica", "B", 10)
	pdf.SetTextColor(255, 255, 255)
	pdf.SetFillColor(50, 50, 100)
	pdf.CellFormat(colW1, 7, "Metric", "1", 0, "L", true, 0, "")
	pdf.CellFormat(colW2, 7, "Value", "1", 1, "R", true, 0, "")

	pdf.SetFont("Helvetica", "", 10)
	for i, row := range summaryData {
		if i%2 == 0 {
			pdf.SetFillColor(240, 240, 248)
		} else {
			pdf.SetFillColor(255, 255, 255)
		}
		pdf.SetTextColor(40, 40, 40)
		pdf.CellFormat(colW1, 6, row[0], "1", 0, "L", true, 0, "")
		pdf.CellFormat(colW2, 6, row[1], "1", 1, "R", true, 0, "")
	}
	pdf.Ln(6)

	// ── Category Breakdown ──────────────────────────────────────────────────
	if len(summary.ByCategory) > 0 {
		pdf.SetFont("Helvetica", "B", 14)
		pdf.SetTextColor(30, 30, 80)
		pdf.CellFormat(pdfBodyW, 9, "Category Breakdown", "", 1, "L", false, 0, "")
		pdf.Ln(1)

		// Sort categories for stable output
		cats := make([]string, 0, len(summary.ByCategory))
		for cat := range summary.ByCategory {
			cats = append(cats, cat)
		}
		sort.Strings(cats)

		catColW := []float64{pdfBodyW * 0.28, pdfBodyW * 0.12, pdfBodyW * 0.12, pdfBodyW * 0.12, pdfBodyW * 0.12, pdfBodyW * 0.12, pdfBodyW * 0.12}
		catHeaders := []string{"Category", "Total", "Pass", "Fail", "Warn", "Unknown", "Score"}

		pdf.SetFont("Helvetica", "B", 9)
		pdf.SetFillColor(50, 50, 100)
		pdf.SetTextColor(255, 255, 255)
		for i, h := range catHeaders {
			pdf.CellFormat(catColW[i], 7, h, "1", 0, "C", true, 0, "")
		}
		pdf.Ln(-1)

		pdf.SetFont("Helvetica", "", 9)
		for i, cat := range cats {
			cs := summary.ByCategory[cat]
			if i%2 == 0 {
				pdf.SetFillColor(240, 240, 248)
			} else {
				pdf.SetFillColor(255, 255, 255)
			}
			pdf.SetTextColor(40, 40, 40)
			row := []string{
				cat,
				fmt.Sprintf("%d", cs.Total),
				fmt.Sprintf("%d", cs.Passing),
				fmt.Sprintf("%d", cs.Failing),
				fmt.Sprintf("%d", cs.Warning),
				fmt.Sprintf("%d", cs.Unknown),
				fmt.Sprintf("%.1f%%", cs.ScorePct),
			}
			for j, cell := range row {
				pdf.CellFormat(catColW[j], pdfSmallLine, cell, "1", 0, "C", true, 0, "")
			}
			pdf.Ln(-1)
		}
		pdf.Ln(6)
	}

	// ── Detail Results Table ─────────────────────────────────────────────────
	if len(results) > 0 {
		pdf.SetFont("Helvetica", "B", 14)
		pdf.SetTextColor(30, 30, 80)
		pdf.CellFormat(pdfBodyW, 9, fmt.Sprintf("Detailed Results (%d checks)", len(results)), "", 1, "L", false, 0, "")
		pdf.Ln(1)

		// Sort results by category then check name for clean grouping
		sort.Slice(results, func(i, j int) bool {
			if results[i].Category != results[j].Category {
				return results[i].Category < results[j].Category
			}
			if results[i].CheckName != results[j].CheckName {
				return results[i].CheckName < results[j].CheckName
			}
			return results[i].ProbeID < results[j].ProbeID
		})

		detColW := []float64{
			pdfBodyW * 0.10, // probe
			pdfBodyW * 0.16, // check
			pdfBodyW * 0.13, // category
			pdfBodyW * 0.10, // severity
			pdfBodyW * 0.09, // status
			pdfBodyW * 0.30, // evidence
			pdfBodyW * 0.12, // timestamp
		}
		detHeaders := []string{"Probe", "Check", "Category", "Severity", "Status", "Evidence", "Timestamp"}

		pdf.SetFont("Helvetica", "B", 8)
		pdf.SetFillColor(50, 50, 100)
		pdf.SetTextColor(255, 255, 255)
		for i, h := range detHeaders {
			pdf.CellFormat(detColW[i], 6, h, "1", 0, "C", true, 0, "")
		}
		pdf.Ln(-1)

		currentCat := ""
		for idx, r := range results {
			// Category sub-header
			if r.Category != currentCat {
				currentCat = r.Category
				pdf.SetFont("Helvetica", "BI", 8)
				pdf.SetFillColor(210, 210, 230)
				pdf.SetTextColor(30, 30, 80)
				pdf.CellFormat(pdfBodyW, 5, "  Category: "+currentCat, "1", 1, "L", true, 0, "")
			}

			if idx%2 == 0 {
				pdf.SetFillColor(248, 248, 255)
			} else {
				pdf.SetFillColor(255, 255, 255)
			}

			r2, g2, b2 := colourForStatus(r.Status)
			evidence := r.Evidence
			if len(evidence) > 60 {
				evidence = evidence[:57] + "..."
			}

			row := []string{
				r.ProbeID,
				r.CheckName,
				r.Category,
				r.Severity,
				r.Status,
				evidence,
				r.Timestamp.UTC().Format("2006-01-02 15:04"),
			}

			pdf.SetFont("Helvetica", "", 7)
			for j, cell := range row {
				if j == 4 { // status column — coloured text
					pdf.SetTextColor(r2, g2, b2)
					pdf.SetFont("Helvetica", "B", 7)
				} else {
					pdf.SetTextColor(40, 40, 40)
					pdf.SetFont("Helvetica", "", 7)
				}
				pdf.CellFormat(detColW[j], pdfSmallLine, cell, "1", 0, "L", true, 0, "")
			}
			pdf.Ln(-1)
		}
	}

	// ── Footer / page numbers ────────────────────────────────────────────────
	pdf.SetFooterFunc(func() {
		pdf.SetY(-12)
		pdf.SetFont("Helvetica", "I", 8)
		pdf.SetTextColor(130, 130, 130)
		pdf.CellFormat(pdfBodyW, 10,
			fmt.Sprintf("Legator Compliance Report — Page %d of {nb}", pdf.PageNo()),
			"", 0, "C", false, 0, "")
	})
	pdf.AliasNbPages("{nb}")

	return pdf.Output(w)
}
