package anonymize

import (
	"bufio"
	"context"
	"encoding/csv"
	"io"
)

// Stage is the ingest pipeline stage (ADR 0005): it structurally satisfies
// assets.Stage — Name is the pipeline record's proof the anonymizer ran,
// and Wrap returns the reader that transforms bytes as they stream to
// storage. The owning org's ID scopes the pseudonym map lookups; the
// upload's format tag picks the cleaning mode.
type Stage struct {
	redactor *Redactor
}

// NewStage returns the ingest anonymization stage resolving pseudonyms
// through r.
func NewStage(r Resolver) *Stage { return &Stage{redactor: NewRedactor(r)} }

// Name is what the Asset's pipeline record shows (the record keeps its
// ticket-04 meaning: "anonymize").
func (s *Stage) Name() string { return "anonymize" }

// Wrap returns the cleaned stream. The reader never holds the whole upload
// in memory, matching ADR 0006's streaming ingest.
func (s *Stage) Wrap(ctx context.Context, orgID, format string, r io.Reader) io.Reader {
	pr, pw := io.Pipe()
	go func() {
		err := s.clean(ctx, orgID, format, r, pw)
		_ = pw.CloseWithError(err) // nil err closes cleanly
	}()
	return pr
}

// clean streams r through the redactor into w. The format tag decides the
// mode: "csv" parses as a table (encoding/csv, so quoted commas and
// newlines survive) with column-aware cleaning; everything else sweeps
// line by line as free text. Identifiers are cleaned in both modes; the
// tag only picks the parsing.
func (s *Stage) clean(ctx context.Context, orgID, format string, r io.Reader, w io.Writer) error {
	bw := bufio.NewWriter(w)
	defer bw.Flush()
	if isCSVFormat(format) {
		return s.cleanCSV(ctx, orgID, bufio.NewReader(r), bw)
	}
	return s.cleanText(ctx, orgID, bufio.NewReader(r), bw)
}

// cleanText sweeps a free-text stream line by line, preserving the
// original line breaks.
func (s *Stage) cleanText(ctx context.Context, orgID string, br *bufio.Reader, bw *bufio.Writer) error {
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			cleaned, cerr := s.redactor.FreeText(ctx, orgID, line)
			if cerr != nil {
				return cerr
			}
			if _, werr := bw.Write(cleaned); werr != nil {
				return werr
			}
		}
		if err != nil {
			return err // io.EOF ends the stream; anything else propagates
		}
	}
}

// cleanCSV parses the stream as a CSV table and cleans it record by
// record, writing as it goes. The header row passes through unchanged —
// it names the columns; the data rows are what carry identifiers.
func (s *Stage) cleanCSV(ctx context.Context, orgID string, br *bufio.Reader, bw *bufio.Writer) error {
	cr := csv.NewReader(br)
	cr.FieldsPerRecord = -1 // tolerate ragged rows rather than refusing data
	cw := csv.NewWriter(bw)

	var classes []string
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			cw.Flush()
			return cw.Error()
		}
		if err != nil {
			return err
		}
		if classes == nil {
			classes = classifyAll(rec)
			if err := cw.Write(rec); err != nil {
				return err
			}
			continue
		}
		cleaned, err := s.redactor.CleanRecord(ctx, orgID, classes, rec)
		if err != nil {
			return err
		}
		if err := cw.Write(cleaned); err != nil {
			return err
		}
	}
}
