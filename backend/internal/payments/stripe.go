package payments

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// The Stripe gateway (ADR 0004): Checkout Sessions for money-in, webhook
// signature verification for callbacks. The HTTP boundary is injectable
// (baseURL) so tests run the gateway against a fake Stripe API, and the
// callback verification is a pure function — both offline.

// stripeAPIBase is Stripe's live API; tests point the gateway elsewhere.
const stripeAPIBase = "https://api.stripe.com"

// StripeGateway implements Gateway for Stripe.
type StripeGateway struct {
	apiKey        string
	webhookSecret string
	returnBase    string // the platform's public URL: success/cancel land here
	baseURL       string // API base, overridable in tests
	client        *http.Client
}

// NewStripe builds the Stripe gateway from the platform config.
func NewStripe(cfg Config) (Gateway, error) {
	if cfg.Provider != ProviderStripe {
		return nil, fmt.Errorf("payments: stripe gateway built for provider %q", cfg.Provider)
	}
	return &StripeGateway{
		apiKey:        cfg.APIKey,
		webhookSecret: cfg.WebhookSecret,
		returnBase:    cfg.ReturnBaseURL,
		baseURL:       stripeAPIBase,
		client:        &http.Client{Timeout: 15 * time.Second},
	}, nil
}

// Provider names the gateway.
func (g *StripeGateway) Provider() string { return ProviderStripe }

// SetBaseURL points the gateway at a different API endpoint — the seam
// tests use to run the real gateway against a fake Stripe API. Production
// wiring never calls it.
func (g *StripeGateway) SetBaseURL(u string) { g.baseURL = u }

// Create opens a Checkout Session for the top-up. The session id is the
// top-up's reference; the hosted payment page is where the consumer pays.
func (g *StripeGateway) Create(ctx context.Context, tu TopUp) (Session, error) {
	form := url.Values{}
	form.Set("mode", "payment")
	// The top-up's ID (minted before this call) rides to Stripe and back on
	// the webhook — one of the two keys (with the session id) joining
	// callback to top-up.
	form.Set("client_reference_id", tu.ID)
	form.Set("success_url", g.returnBase+"/payments/return?status=success")
	form.Set("cancel_url", g.returnBase+"/payments/return?status=cancelled")
	// Stripe wants smallest-currency-unit integers — exactly AmountMinor.
	form.Set("line_items[0][price_data][currency]", tu.Currency)
	form.Set("line_items[0][price_data][product_data][name]", "Thresh credits top-up")
	form.Set("line_items[0][price_data][unit_amount]", strconv.FormatInt(tu.AmountMinor, 10))
	form.Set("line_items[0][quantity]", "1")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.baseURL+"/v1/checkout/sessions", strings.NewReader(form.Encode()))
	if err != nil {
		return Session{}, fmt.Errorf("payments: build stripe request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+g.apiKey)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := g.client.Do(req)
	if err != nil {
		return Session{}, fmt.Errorf("payments: stripe session request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Session{}, fmt.Errorf("payments: read stripe response: %w", err)
	}
	var doc struct {
		ID    string `json:"id"`
		URL   string `json:"url"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return Session{}, fmt.Errorf("payments: stripe response is not JSON: %w", err)
	}
	if resp.StatusCode != http.StatusOK || doc.Error != nil {
		msg := "stripe error"
		if doc.Error != nil {
			msg = doc.Error.Message
		}
		return Session{}, fmt.Errorf("payments: stripe refused the session (http %d): %s", resp.StatusCode, msg)
	}
	return Session{Reference: doc.ID, PaymentURL: doc.URL}, nil
}

// Stripe webhook outcomes we translate. Everything else is ignored
// (answered as a rejected callback the API surfaces as 400 — Stripe stops
// retrying non-2xx only on 4xx for signature failures, so unknown events
// deliberately 200 at the API layer; here they read as "nothing to do").
var stripeOutcome = map[string]Outcome{
	"checkout.session.completed":               OutcomePaid,
	"checkout.session.async_payment_succeeded": OutcomePaid,
	"checkout.session.async_payment_failed":    OutcomeFailed,
	"checkout.session.expired":                 OutcomeCancelled,
}

// ParseCallback verifies the Stripe-Signature header (the v1 HMAC scheme)
// before parsing the event, and translates the event type to an outcome
// with the session id and captured amount.
func (g *StripeGateway) ParseCallback(_ context.Context, header http.Header, body []byte) (Callback, error) {
	sig := header.Get("Stripe-Signature")
	if err := verifyStripeSignature(sig, g.webhookSecret, body, time.Now()); err != nil {
		return Callback{}, err
	}

	var event struct {
		Type string `json:"type"`
		Data struct {
			Object struct {
				ID          string `json:"id"`
				AmountTotal int64  `json:"amount_total"`
			} `json:"object"`
			AmountTotal int64 `json:"amount_total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &event); err != nil {
		return Callback{}, fmt.Errorf("%w: body is not a Stripe event", ErrCallbackRejected)
	}
	outcome, known := stripeOutcome[event.Type]
	if !known {
		return Callback{}, errUnhandledEvent{eventType: event.Type}
	}
	// Stripe's checkout events nest the session in data.object; some event
	// families hoist an amount_total onto data itself. Prefer the object's
	// captured amount, fall back to the hoisted one.
	amount := event.Data.Object.AmountTotal
	if amount == 0 {
		amount = event.Data.AmountTotal
	}
	return Callback{
		Reference:   event.Data.Object.ID,
		Outcome:     outcome,
		AmountMinor: amount,
	}, nil
}

// errUnhandledEvent marks a verified event we don't act on. The caller
// (API layer) answers 200 so the gateway stops retrying; nothing settles.
type errUnhandledEvent struct{ eventType string }

func (e errUnhandledEvent) Error() string { return "payments: unhandled gateway event " + e.eventType }

// IsUnhandledEvent reports whether the callback carried a verified but
// irrelevant gateway event — the webhook should answer 200 and move on.
func IsUnhandledEvent(err error) bool {
	var target errUnhandledEvent
	return errors.As(err, &target)
}

// verifyStripeSignature checks a Stripe-Signature header against the
// webhook secret: the v1 scheme signs "{timestamp}.{body}" with
// HMAC-SHA256, and fresh timestamps only — the tolerance window rejects
// captured-and-replayed deliveries older than five minutes. Constant-time
// compares throughout.
func verifyStripeSignature(sigHeader, secret string, body []byte, now time.Time) error {
	if sigHeader == "" {
		return fmt.Errorf("%w: missing Stripe-Signature header", ErrCallbackRejected)
	}
	var timestamp, signed string
	for _, part := range strings.Split(sigHeader, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "t":
			timestamp = kv[1]
		case "v1":
			signed = kv[1]
		}
	}
	if timestamp == "" || signed == "" {
		return fmt.Errorf("%w: signature header lacks t/v1 parts", ErrCallbackRejected)
	}
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return fmt.Errorf("%w: unreadable signature timestamp", ErrCallbackRejected)
	}
	age := now.Unix() - ts
	if age < 0 {
		age = -age
	}
	if age > 300 { // Stripe's own five-minute tolerance
		return fmt.Errorf("%w: signature timestamp outside the tolerance window", ErrCallbackRejected)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	want := mac.Sum(nil)
	got, err := hex.DecodeString(signed)
	if err != nil {
		return fmt.Errorf("%w: signature is not hex", ErrCallbackRejected)
	}
	if !hmac.Equal(got, want) {
		return fmt.Errorf("%w: signature mismatch", ErrCallbackRejected)
	}
	return nil
}
