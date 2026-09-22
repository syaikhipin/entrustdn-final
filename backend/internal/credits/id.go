package credits

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// randomSuffix disambiguates movement IDs minted within the same nanosecond.
// A crypto/rand failure is returned, never papered over: an ID with less
// entropy than designed is a silent corruption of the ledger's keys.
func randomSuffix() (string, error) {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("credits: mint movement id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
