package assets

import (
	"context"
	"fmt"
	"io"
)

// Stage is one step of the ingest pipeline (ADR 0005: anonymization is a
// pipeline stage at ingest, not an uploader's discipline). A stage wraps the
// stream flowing from the uploader to storage; Name carries the stage's
// name for the Asset's pipeline record. Wrap receives the owning org's ID
// (pseudonymization keys on it) and the upload's format tag (cleaning mode:
// a csv stream parses as columns, anything else sweeps as free text).
type Stage interface {
	Name() string
	// Wrap returns the reader the next stage (or storage) sees, scoped to
	// the owning organization and the declared format.
	Wrap(ctx context.Context, orgID, format string, r io.Reader) io.Reader
}

// Pipeline is the ordered set of stages every upload passes through.
type Pipeline struct {
	stages []Stage
}

// NewPipeline returns a pipeline running the given stages in order.
func NewPipeline(stages ...Stage) *Pipeline {
	return &Pipeline{stages: stages}
}

// Names lists the stage names in run order — what an Asset's Pipeline field
// records at ingest.
func (p *Pipeline) Names() []string {
	names := make([]string, len(p.stages))
	for i, s := range p.stages {
		names[i] = s.Name()
	}
	return names
}

// RunFor wraps r through every stage in order on behalf of one org and
// reports what ran. The returned reader is the fully-pipelined stream;
// storage receives the last stage's output.
func (p *Pipeline) RunFor(ctx context.Context, orgID, format string, r io.Reader) (io.Reader, []string, error) {
	for _, s := range p.stages {
		if s == nil {
			return nil, nil, fmt.Errorf("assets: nil pipeline stage")
		}
		r = s.Wrap(ctx, orgID, format, r)
	}
	return r, p.Names(), nil
}

// PassThrough is gone: since ticket 05 the anonymize stage is the real
// implementation, built by the anonymize package and handed to
// assets.NewPipeline — one Stage interface, structurally satisfied.
