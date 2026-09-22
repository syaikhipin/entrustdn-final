package membership

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// newTokenID returns a random hex identifier with a short prefix, used for
// in-memory IDs (the postgres Store lets the database generate UUIDs).
func newTokenID(prefix string) string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		// A crypto/rand failure is unrecoverable; fall back to a
		// time-derived value rather than colliding silently.
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return prefix + "-" + hex.EncodeToString(buf)
}
