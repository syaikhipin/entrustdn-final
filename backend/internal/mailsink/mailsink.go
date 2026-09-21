// Package mailsink is the outbound-email seam. At pilot stage (dev mode) mail
// is not sent: verification links land in the server log. A real SMTP sink
// slots in behind the same interface later without touching callers.
package mailsink

import (
	"fmt"
	"io"
)

// Sink sends transactional email.
type Sink interface {
	SendVerification(to, link string) error
}

// LogSink writes email to an io.Writer — the dev-mode log sink.
type LogSink struct {
	w io.Writer
}

// NewLogSink returns a Sink writing to w (typically the server's log).
func NewLogSink(w io.Writer) *LogSink {
	return &LogSink{w: w}
}

// SendVerification records a verification email in the log.
func (s *LogSink) SendVerification(to, link string) error {
	_, err := fmt.Fprintf(s.w, "[mail] verification link for %s: %s\n", to, link)
	if err != nil {
		return fmt.Errorf("failed to write verification mail to log: %w", err)
	}
	return nil
}
