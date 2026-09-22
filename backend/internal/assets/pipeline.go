package assets

import (
	"fmt"
	"io"
)

// Stage is one step of the ingest pipeline (ADR 0005: anonymization is a
// pipeline stage at ingest, not an uploader's discipline). A stage wraps the
// stream flowing from the uploader to storage; Wrapping carries the stage's
// name for the Asset's pipeline record.
type Stage interface {
	Name() string
	// Wrap returns the reader the next stage (or storage) sees.
	Wrap(r io.Reader) io.Reader
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

// Run wraps r through every stage in order and reports what ran. The
// returned reader is the fully-pipelined stream; storage receives the last
// stage's output.
func (p *Pipeline) Run(r io.Reader) (io.Reader, []string, error) {
	for _, s := range p.stages {
		if s == nil {
			return nil, nil, fmt.Errorf("assets: nil pipeline stage")
		}
		r = s.Wrap(r)
	}
	return r, p.Names(), nil
}

// PassThrough is the anonymization stage as ticket 04 ships it: present and
// recorded on every ingest, but not yet transforming bytes — real
// identifier stripping lands in ticket 05. Its presence is the enforcement
// point: ticket 05 replaces the body without touching the pipeline or any
// caller.
type PassThrough struct{}

// Compile-time check: the pass-through stage is a Stage.
var _ Stage = (*PassThrough)(nil)

// Name is what the Asset's pipeline record shows.
func (PassThrough) Name() string { return "anonymize" }

// Wrap returns r unchanged — pass-through until ticket 05.
func (PassThrough) Wrap(r io.Reader) io.Reader { return r }
