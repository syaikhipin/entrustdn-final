// Package anonymize implements ADR 0005: personal identifiers are stripped
// or pseudonymized at Data Asset ingest and again before Collection
// delivery, so shared data cannot identify a Farmer Member. Pseudonyms are
// resolved through a Resolver — the pseudonym map lives in-platform only
// (the org's boundary) and never appears in delivered data. Precision wins
// over recall: the redactor refuses to mangle ordinary numbers, so its
// identifier patterns are deliberately conservative.
package anonymize

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Resolver hands out stable pseudonyms for one identifier on behalf of one
// org. The Postgres-backed implementation owns the pseudonym map; tests
// substitute fakes. Same (org, kind, value) → same pseudonym, so pseudonymed
// data stays linkable within the org; erasing the map entry re-mints.
type Resolver interface {
	Pseudonym(ctx context.Context, orgID, kind, value string) (string, error)
}

// emailRegex matches an ordinary email address.
var emailRegex = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)

// phoneRegexes match Irish phone shapes: international (+353…) and domestic
// 08x mobile/landline with or without spacing. Deliberately narrow — dates,
// prices, and sizes must survive untouched.
var phoneRegexes = []*regexp.Regexp{
	regexp.MustCompile(`\+353\s?(?:1|\d{2})\s?\d{3}\s?\d{4}\b`),
	// 08x + 7 digits, spaces allowed between digit groups ("087 123 4567").
	regexp.MustCompile(`\b08[3-9](?:\s?\d){7}\b`),
}

// coordPairRegex matches a lat,lon pair with GPS-grade precision (3+
// decimals each). Two decimals is city scale — not identifying — and
// refusing it keeps "1,2" or "52.12, was" from being mangled.
var coordPairRegex = regexp.MustCompile(`(-?\d{1,3}\.\d{3,})\s*,\s*(-?\d{1,3}\.\d{3,})`)

// coordLabeledRegex matches a single labeled coordinate with GPS-grade
// precision: "lat: 52.123456". Unlabeled short decimals stay untouched.
var coordLabeledRegex = regexp.MustCompile(`(?i)\b(lat|latitude|lon|long|longitude)\s*[:=]\s*(-?\d{1,3}\.\d{3,})`)

// titledNameRegex matches "Title First Last" — the name shape free text
// carries when a title is present. The title stays; the name is
// pseudonymized.
var titledNameRegex = regexp.MustCompile(`\b(Mrs?|Ms|Dr|Prof)\.?\s+([A-Z][a-z]+)\s+([A-Z][a-z]+)\b`)

// cuedNameRegex matches an untitled "First Last" preceded by a role word —
// the common case in farm logs ("farmer Mary Byrne", "signed by Pat
// Smith"). Irish surname apostrophes (O'Brien) are allowed. Without a cue,
// capitalized pairs are left alone: "Spring Barley" is a crop, not a
// farmer. Precision over recall.
var cuedNameRegex = regexp.MustCompile(`(?i)\b(farmer|member|signed by|contact|witnessed by)\s+([A-Z][a-z]+)\s+([A-Z][a-z]*(?:'[A-Z][a-z]*)?)\b`)

// Redactor cleans one line of free text through the identifier patterns.
type Redactor struct {
	resolver Resolver
}

// NewRedactor returns a Redactor resolving pseudonyms through r.
func NewRedactor(r Resolver) *Redactor { return &Redactor{resolver: r} }

// FreeText pseudonymizes emails, phones, and titled names, and coarsens
// coordinates, in one line of bytes. Lines that carry no identifier come
// back unchanged.
func (r *Redactor) FreeText(ctx context.Context, orgID string, line []byte) ([]byte, error) {
	s := string(line)

	// Coordinates first, so their coarsened decimals can never re-match a
	// phone or name pattern downstream.
	s = coordPairRegex.ReplaceAllStringFunc(s, func(m string) string {
		parts := coordPairRegex.FindStringSubmatch(m)
		return coarseCoord(parts[1]) + ", " + coarseCoord(parts[2])
	})
	s = coordLabeledRegex.ReplaceAllStringFunc(s, func(m string) string {
		parts := coordLabeledRegex.FindStringSubmatch(m)
		return parts[1] + ": " + coarseCoord(parts[2])
	})

	// Emails and phones are pseudonymized through the map.
	var err error
	s, err = r.replace(ctx, orgID, s, emailRegex, "email", func(m []string) string { return m[0] }, nil)
	if err != nil {
		return nil, err
	}
	for _, re := range phoneRegexes {
		s, err = r.replace(ctx, orgID, s, re, "phone", func(m []string) string { return m[0] }, nil)
		if err != nil {
			return nil, err
		}
	}

	// Names: the map learns the bare name; the cue or title stays in the
	// text. Titled names first so the cued pass cannot pre-empt them.
	s, err = r.replace(ctx, orgID, s, titledNameRegex, "name",
		func(m []string) string { return m[2] + " " + m[3] },
		func(m []string, tok string) string { return m[1] + " " + tok })
	if err != nil {
		return nil, err
	}
	s, err = r.replace(ctx, orgID, s, cuedNameRegex, "name",
		func(m []string) string { return m[2] + " " + m[3] },
		func(m []string, tok string) string { return m[1] + " " + tok })
	if err != nil {
		return nil, err
	}
	return []byte(s), nil
}

// replace pseudonymizes every match of re through the resolver. Same value
// → same lookup → the resolver's stability gives same-text stability. A
// resolver failure aborts the whole line: shipping a half-cleaned line is
// exactly the leak ADR 0005 forbids. render, when set, rebuilds the
// replacement from the submatches and token (names keep their title);
// nil means the token replaces the whole match.
func (r *Redactor) replace(ctx context.Context, orgID, s string, re *regexp.Regexp, kind string, value func([]string) string, render func([]string, string) string) (string, error) {
	var firstErr error
	out := re.ReplaceAllStringFunc(s, func(m string) string {
		if firstErr != nil {
			return m
		}
		sub := re.FindStringSubmatch(m)
		tok, err := r.resolver.Pseudonym(ctx, orgID, kind, value(sub))
		if err != nil {
			firstErr = fmt.Errorf("anonymize: resolve %s: %w", kind, err)
			return m
		}
		if render != nil {
			return render(sub, tok)
		}
		return tok
	})
	if firstErr != nil {
		return "", firstErr
	}
	return out, nil
}

// coarseCoord rounds one coordinate string to one decimal — GPS precision
// down to ~11 km, useless for identifying a field, useful for a region.
func coarseCoord(s string) string {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return s
	}
	return strconv.FormatFloat(math.Round(f*10)/10, 'f', 1, 64)
}
