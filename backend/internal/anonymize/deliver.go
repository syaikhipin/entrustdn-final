package anonymize

import (
	"bytes"
	"context"
	"fmt"
)

// Delivery is the outbound half of ADR 0005: nothing reaches a Data
// Consumer until it has been through the same cleaning the ingest
// pipeline applies. The pseudonym map itself is never part of any payload
// — delivered data carries pseudonyms only, resolved through the map.
//
// The choke point is the type system, not a convention: a payload can
// cross the delivery boundary only as a CleanedCollection, and the only
// way to mint one is Collection() — its cleaning has already run. A future
// delivery route that streams raw bytes would need to deliberately
// reconstruct the bytes it was handed, not merely forget to call a
// function.
type Delivery struct {
	redactor *Redactor
}

// NewDelivery returns the delivery cleaner resolving pseudonyms through m.
func NewDelivery(m Map) *Delivery { return &Delivery{redactor: NewRedactor(m)} }

// CleanedCollection is a Collection output whose delivery-side anonymization
// has run (ADR 0005). Construction is package-private, so every value a
// caller holds has been cleaned; Bytes is what may leave the platform.
type CleanedCollection struct {
	bytes []byte
}

// Bytes returns the cleaned payload for delivery.
func (c CleanedCollection) Bytes() []byte { return c.bytes }

// Collection cleans one Collection output on behalf of its owning org,
// ready for delivery to the Data Consumer. Format picks the mode exactly
// as ingest does ("csv" parses columns; anything else sweeps as free
// text). A cleaning failure refuses the delivery rather than shipping
// possibly-identifying data.
func (d *Delivery) Collection(ctx context.Context, orgID, format string, payload []byte) (CleanedCollection, error) {
	if len(payload) == 0 {
		return CleanedCollection{}, fmt.Errorf("anonymize: refuse to deliver an empty collection")
	}
	var cleaned []byte
	var err error
	if isCSVFormat(format) {
		cleaned, err = d.redactor.CSV(ctx, orgID, payload)
		if err != nil {
			return CleanedCollection{}, err
		}
		return CleanedCollection{bytes: cleaned}, nil
	}
	// Free text: line-preserving sweep of the whole payload.
	var out bytes.Buffer
	for _, line := range bytes.SplitAfter(payload, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		cleaned, err = d.redactor.FreeText(ctx, orgID, line)
		if err != nil {
			return CleanedCollection{}, err
		}
		if _, err := out.Write(cleaned); err != nil {
			return CleanedCollection{}, err
		}
	}
	return CleanedCollection{bytes: out.Bytes()}, nil
}
