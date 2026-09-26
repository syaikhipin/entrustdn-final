// Package payments implements ticket 09: top-ups through an
// admin-configured Payment Gateway (ADR 0004). External money enters as a
// Top-up only — Revenue Shares stay internal (ADR 0002) — and the gateway
// is platform configuration, never a Module: money flows are not
// third-party extensible. Every settled top-up posts through the Ledger
// (credits.KindTopUp); failed and cancelled payments never credit, and a
// replayed callback credits nothing more than the first delivery.
package payments

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/syaikhipin/entrustdn-final/backend/internal/credits"
)

// Provider names the supported gateways. Stripe is implemented; PayPal
// slots into the Registry behind the same Gateway seam.
const ProviderStripe = "stripe"

// Status is the life cycle of one top-up. Pending until the gateway
// reports an outcome; failed/cancelled are terminal refusals; settled is
// terminal success — the Ledger movement is already posted.
type TopUpStatus string

const (
	StatusPending   TopUpStatus = "pending"
	StatusSettled   TopUpStatus = "settled"
	StatusFailed    TopUpStatus = "failed"
	StatusCancelled TopUpStatus = "cancelled"
)

// terminal reports whether the status closes the top-up: a terminal top-up
// never transitions again, whatever later callbacks claim.
func (s TopUpStatus) terminal() bool {
	return s == StatusSettled || s == StatusFailed || s == StatusCancelled
}

// Terminal reports whether the status closes the top-up (exported for the
// postgres store, which re-checks transitions inside its settlement
// transaction).
func (s TopUpStatus) Terminal() bool { return s.terminal() }

// Outcome is what a verified gateway callback reports.
type Outcome string

const (
	OutcomePaid      Outcome = "paid"
	OutcomeFailed    Outcome = "failed"
	OutcomeCancelled Outcome = "cancelled"
)

// Sentinel errors. Callers match with errors.Is; ErrCallbackRejected means
// the payload failed verification before it was trusted with anything.
var (
	// ErrDisabled is returned when no gateway is configured: top-ups are
	// off, and the config surface refuses partial credentials.
	ErrDisabled = errors.New("payments: no payment gateway is configured")
	// ErrNotFound is returned for references and top-ups that do not exist.
	ErrNotFound = errors.New("payments: no such top-up")
	// ErrCallbackRejected covers every untrustworthy callback: a failed
	// signature, a malformed body, an unverifiable payload. The top-up's
	// state is untouched when it fires.
	ErrCallbackRejected = errors.New("payments: callback rejected")
	// ErrAmountMismatch fires when a verified paid callback's amount
	// disagrees with the session the gateway opened: refuse, stay pending.
	ErrAmountMismatch = errors.New("payments: callback amount does not match the top-up")
	// ErrAlreadyTerminal fires when a callback contradicts a closed top-up
	// (paid after failed): loud refusal, never a silent swallow.
	ErrAlreadyTerminal = errors.New("payments: top-up is already closed")
)

// Config is the server-side platform configuration of the gateway
// (ADR 0004: credentials never leave the server; Module Authors can never
// touch payment paths). A zero Config means top-ups are disabled.
type Config struct {
	// Provider selects the gateway implementation ("stripe"; "paypal" later).
	Provider string `json:"provider"`
	// APIKey authenticates the platform to the gateway.
	APIKey string `json:"-"`
	// WebhookSecret verifies callback signatures.
	WebhookSecret string `json:"-"`
	// Currency is the three-letter code the platform charges in (ISO 4217).
	Currency string `json:"currency"`
	// ReturnBaseURL prefixes the gateway's success/cancel redirect URLs —
	// the deployment's public URL (THRESH_PUBLIC_BASE_URL).
	ReturnBaseURL string `json:"return_base_url,omitempty"`
	// MicrosPerCent is the exchange rate: how many micro-credits one
	// smallest-currency-unit (one cent) buys. The Platform Admin's
	// pricing knob — set 10_000 for 1 EUR = 1 credit.
	MicrosPerCent int64 `json:"micros_per_cent"`
}

// Enabled reports whether a gateway is fully configured. Any partial
// configuration counts as disabled: a half-set gateway is a
// misconfiguration, not a degraded mode.
func (c Config) Enabled() bool {
	return c.Provider != "" && c.APIKey != "" && c.WebhookSecret != "" &&
		c.Currency != "" && c.MicrosPerCent > 0 && c.ReturnBaseURL != ""
}

// Validate refuses configurations that cannot work — structurally. Whether
// the provider is one the platform can actually build is the Registry's
// call (SaveConfig proves it by building the gateway), so a compiled-in
// provider list doesn't live here.
func (c Config) Validate() error {
	if c.Provider == "" {
		return errors.New("payments: provider must be set (e.g. stripe)")
	}
	if c.APIKey == "" {
		return errors.New("payments: api key must be set")
	}
	if c.WebhookSecret == "" {
		return errors.New("payments: webhook secret must be set")
	}
	if len(c.Currency) != 3 || c.Currency != strings.ToLower(c.Currency) {
		return errors.New("payments: currency must be a three-letter lowercase ISO 4217 code")
	}
	if c.MicrosPerCent <= 0 {
		return errors.New("payments: micros_per_cent must be positive")
	}
	if c.ReturnBaseURL == "" {
		return errors.New("payments: return_base_url must be set (THRESH_PUBLIC_BASE_URL) — the gateway needs somewhere to send the payer after checkout")
	}
	if _, err := url.ParseRequestURI(c.ReturnBaseURL); err != nil {
		return fmt.Errorf("payments: return_base_url %q is not a valid absolute URL", c.ReturnBaseURL)
	}
	return nil
}

// TopUp is one money-in attempt: opened at Initiate, closed by the first
// terminal callback. The ID is minted before the gateway session opens so
// it can ride to the gateway as the callback's return key; Reference is
// the gateway's own session id. CreditsMicros is the ledger side — what
// settlement will credit — derived at initiation from the minor units and
// the rate, so the consumer sees the credit amount before paying.
type TopUp struct {
	ID            string      `json:"id"`
	AccountID     string      `json:"account_id"`
	Reference     string      `json:"reference"` // the gateway's session id
	Provider      string      `json:"provider"`
	AmountMinor   int64       `json:"amount_minor"` // gateway amount in smallest units
	Currency      string      `json:"currency"`
	CreditsMicros int64       `json:"credits_micros"`
	Status        TopUpStatus `json:"status"`
	MovementID    string      `json:"movement_id,omitempty"` // set once settled
	PaymentURL    string      `json:"payment_url,omitempty"` // where the consumer pays
	CreatedAt     time.Time   `json:"created_at"`
	UpdatedAt     time.Time   `json:"updated_at"`
}

// Session is what a gateway returns when a top-up is opened.
type Session struct {
	Reference  string
	PaymentURL string
}

// Callback is a verified gateway report: which session, what outcome, and
// the amount the gateway actually captured (matching is the caller's
// check — AmountMinor is what the gateway says, TopUp is what we opened).
type Callback struct {
	Reference   string
	Outcome     Outcome
	AmountMinor int64
}

// Gateway is the provider seam (ADR 0004's pluggable gateway). Create
// opens a payment session; ParseCallback verifies the callback's
// signature and decodes it — an unverifiable payload returns
// ErrCallbackRejected and is never trusted. The real Stripe gateway
// satisfies this; tests substitute a fake.
type Gateway interface {
	Provider() string
	Create(ctx context.Context, tu TopUp) (Session, error)
	ParseCallback(ctx context.Context, header http.Header, body []byte) (Callback, error)
}

// CreditLedger is the slice of the ledger the payments package needs: only
// posting movements. It is satisfied by the same credits.Store the api
// package serves balances from, so top-ups and balances always read the
// one ledger.
type CreditLedger interface {
	PostMovement(ctx context.Context, mov credits.Movement) (credits.Movement, error)
}
