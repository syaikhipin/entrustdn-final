package mailsink_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/syaikhipin/entrustdn-final/backend/internal/mailsink"
)

// Dev mail sink: at pilot there is no mail server; verification links land
// in the server log (spec: Membership). The sink is the seam ticket 02's
// email-verification tests will assert against — "Email asserts on the dev
// log sink" (Seam 3, cross-cutting).

func TestLogSinkWritesVerificationLink(t *testing.T) {
	tests := []struct {
		name string
		to   string
		link string
	}{
		{name: "consumer signup", to: "analyst@example.org", link: "https://thresh.dev/verify?token=abc123"},
		{name: "org signup", to: "org@irishfarms.ie", link: "https://thresh.dev/verify?token=def456"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			sink := mailsink.NewLogSink(&buf)

			if err := sink.SendVerification(tt.to, tt.link); err != nil {
				t.Fatalf("SendVerification() error = %v", err)
			}

			out := buf.String()
			if !strings.Contains(out, tt.to) {
				t.Errorf("log output does not mention recipient %q:\n%s", tt.to, out)
			}
			if !strings.Contains(out, tt.link) {
				t.Errorf("log output does not contain verification link %q:\n%s", tt.link, out)
			}
		})
	}
}
