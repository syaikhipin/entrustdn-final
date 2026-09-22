package anonymize

import (
	"context"
	"encoding/csv"
	"fmt"
	"strings"
)

// Column-name vocabularies for structured data. A column whose header
// segments match one of these is treated wholesale: name-like columns are
// pseudonymized value-by-value, coordinate columns coarsened. Everything
// unrecognized still gets the free-text sweep.
var coordColumns = map[string]bool{
	"latitude": true, "longitude": true, "lat": true, "lon": true, "long": true, "lng": true,
}

// nameSegments are the header words that mark a name column.
var nameSegments = map[string]bool{
	"name": true, "farmer": true, "member": true, "owner": true,
}

// classifyColumn decides how a column is cleaned: "name" (pseudonymize
// values), "coord" (coarsen), or "" (free-text sweep only). Headers are
// split into word segments, so "plot_lat" and "farmer_name" classify too.
func classifyColumn(header string) string {
	segs := headerSegments(strings.ToLower(strings.TrimSpace(header)))
	for _, seg := range segs {
		if coordColumns[seg] {
			return "coord"
		}
	}
	for _, seg := range segs {
		if nameSegments[seg] {
			return "name"
		}
	}
	return ""
}

// headerSegments splits a lowercase header into word fragments on _ - . and
// spaces: "plot_latitude" → ["plot", "latitude"].
func headerSegments(h string) []string {
	return strings.FieldsFunc(h, func(r rune) bool {
		return r == '_' || r == '-' || r == '.' || r == ' '
	})
}

// classifyAll classifies a header row: one column class per column.
func classifyAll(header []string) []string {
	classes := make([]string, len(header))
	for i, h := range header {
		classes[i] = classifyColumn(h)
	}
	return classes
}

// isCSVFormat reports whether a format tag means structured cleaning.
func isCSVFormat(format string) bool {
	return strings.EqualFold(strings.TrimSpace(format), "csv")
}

// CleanRecord cleans one CSV record against its column classes: name
// columns pseudonymize the whole cell, coordinate columns coarsen, and
// every other cell also gets the free-text sweep so identifiers hidden in
// notes columns do not survive. A resolver failure aborts the record —
// shipping a half-cleaned row is exactly the leak ADR 0005 forbids.
func (r *Redactor) CleanRecord(ctx context.Context, orgID string, classes []string, rec []string) ([]string, error) {
	for ci, cell := range rec {
		if cell == "" {
			continue
		}
		var cls string
		if ci < len(classes) {
			cls = classes[ci]
		}
		switch cls {
		case "name":
			tok, err := r.resolver.Pseudonym(ctx, orgID, "name", cell)
			if err != nil {
				return nil, fmt.Errorf("anonymize: resolve name column: %w", err)
			}
			rec[ci] = tok
		case "coord":
			rec[ci] = coarseCoord(cell)
		default:
			cleaned, err := r.FreeText(ctx, orgID, []byte(cell))
			if err != nil {
				return nil, fmt.Errorf("anonymize: sweep cell: %w", err)
			}
			rec[ci] = string(cleaned)
		}
	}
	return rec, nil
}

// CSV cleans a whole in-memory CSV table: column classes come from the
// header row, every data record goes through CleanRecord. The streaming
// Stage path (cleanCSV) is preferred for large payloads; CSV is the
// whole-payload form Delivery uses.
func (r *Redactor) CSV(ctx context.Context, orgID string, data []byte) ([]byte, error) {
	cr := csv.NewReader(strings.NewReader(string(data)))
	cr.FieldsPerRecord = -1 // tolerate ragged rows rather than refusing data
	records, err := cr.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("anonymize: parse csv: %w", err)
	}
	if len(records) == 0 {
		return data, nil
	}

	classes := classifyAll(records[0])
	for ri, rec := range records {
		if ri == 0 {
			continue // header row carries no data
		}
		cleaned, err := r.CleanRecord(ctx, orgID, classes, rec)
		if err != nil {
			return nil, err
		}
		records[ri] = cleaned
	}

	var out strings.Builder
	cw := csv.NewWriter(&out)
	if err := cw.WriteAll(records); err != nil {
		return nil, fmt.Errorf("anonymize: write csv: %w", err)
	}
	return []byte(out.String()), nil
}
