package payments

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The PayPal gateway (ticket 17): Orders v2 for money-in, certificate-based
// webhook verification for callbacks. Unlike Stripe's shared-secret HMAC,
// PayPal signs each delivery with an RSA key whose certificate the
// PAYPAL-CERT-URL header names; verification rebuilds the signed string —
// transmission id | transmission time | webhook id | crc32(body) — and
// checks it against the certificate's public key. The HTTP boundary is
// injectable (baseURL, like Stripe) so tests run the gateway against a
// fake PayPal API.
//
// Flow: Create opens an order; the buyer approves it on PayPal's hosted
// page. PayPal's CHECKOUT.ORDER.APPROVED webhook triggers the server-side
// capture (Orders v2 money moves only on an explicit capture), answered as
// an unhandled event; the capture's own PAYMENT.CAPTURE.COMPLETED webhook
// carries the amount and settles the top-up — or DENIED/DECLINED marks it
// failed. Settlement itself is the Service's job, idempotent through the
// store's atomic claim.

// paypalAPIBase is PayPal's live API; tests point the gateway elsewhere.
const paypalAPIBase = "https://api-m.paypal.com"

// PayPalGateway implements Gateway for PayPal.
type PayPalGateway struct {
	clientID     string // OAuth2 client id
	clientSecret string // OAuth2 client secret
	webhookID    string // PayPal's identifier for OUR webhook registration
	returnBase   string // the platform's public URL: approve/cancel land here
	baseURL      string // API base, overridable in tests
	client       *http.Client

	// certHostOK decides whether a callback's PAYPAL-CERT-URL may be
	// fetched. The default pins the host to *.paypal.com — an attacker-
	// chosen certificate URL must never be trusted. Tests replace the
	// predicate to point at a fake API's host; production wiring never
	// calls the setter.
	certHostOK func(certURL string) error

	mu      sync.Mutex
	token   string
	tokenAt time.Time
	certs   map[string]*certEntry
}

type certEntry struct {
	cert *x509.Certificate
	at   time.Time
}

// certCacheTTL bounds how long a fetched certificate is trusted from
// cache; PayPal rotates certificates rarely but the header names the right
// one, so a miss simply refetches.
const certCacheTTL = 24 * time.Hour

// NewPayPal builds the PayPal gateway from the platform config.
func NewPayPal(cfg Config) (Gateway, error) {
	if cfg.Provider != ProviderPayPal {
		return nil, fmt.Errorf("payments: paypal gateway built for provider %q", cfg.Provider)
	}
	if cfg.WebhookID == "" {
		return nil, fmt.Errorf("payments: paypal gateway needs the webhook id")
	}
	g := &PayPalGateway{
		clientID:     cfg.APIKey,
		clientSecret: cfg.WebhookSecret,
		webhookID:    cfg.WebhookID,
		returnBase:   cfg.ReturnBaseURL,
		baseURL:      paypalAPIBase,
		client:       &http.Client{Timeout: 15 * time.Second},
		certs:        map[string]*certEntry{},
	}
	g.certHostOK = defaultCertHostOK
	return g, nil
}

// Provider names the gateway.
func (g *PayPalGateway) Provider() string { return ProviderPayPal }

// SetBaseURL points the gateway at a different API endpoint — the seam
// tests use to run the real gateway against a fake PayPal API. Production
// wiring never calls it.
func (g *PayPalGateway) SetBaseURL(u string) { g.baseURL = u }

// SetCertHostCheck replaces the certificate-URL policy — the test seam for
// serving webhook certificates from a fake API. Production wiring never
// calls it.
func (g *PayPalGateway) SetCertHostCheck(ok func(certURL string) error) { g.certHostOK = ok }

// defaultCertHostOK pins certificate fetches to PayPal's own hosts: https
// only, and the host must be paypal.com or a subdomain.
func defaultCertHostOK(certURL string) error {
	u, err := url.Parse(certURL)
	if err != nil {
		return fmt.Errorf("unreadable certificate url: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("certificate url %q is not https", certURL)
	}
	host := strings.ToLower(u.Hostname())
	if host != "paypal.com" && !strings.HasSuffix(host, ".paypal.com") {
		return fmt.Errorf("certificate host %q is outside paypal.com", host)
	}
	return nil
}

// --- OAuth2 ---

// accessToken returns a valid OAuth2 token, caching it until shortly
// before expiry (the client-credentials grant; PayPal tokens live ~9
// hours, the cache just avoids a round trip per call).
func (g *PayPalGateway) accessToken(ctx context.Context) (string, error) {
	g.mu.Lock()
	if g.token != "" && time.Since(g.tokenAt) < 8*time.Hour {
		t := g.token
		g.mu.Unlock()
		return t, nil
	}
	g.mu.Unlock()

	form := strings.NewReader("grant_type=client_credentials")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.baseURL+"/v1/oauth2/token", form)
	if err != nil {
		return "", fmt.Errorf("payments: build paypal token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString(
		[]byte(g.clientID+":"+g.clientSecret)))

	resp, err := g.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("payments: paypal token request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("payments: read paypal token response: %w", err)
	}
	var doc struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &doc); err != nil || doc.AccessToken == "" {
		return "", fmt.Errorf("payments: paypal token response unreadable (http %d)", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("payments: paypal refused the token request (http %d)", resp.StatusCode)
	}

	g.mu.Lock()
	g.token = doc.AccessToken
	g.tokenAt = time.Now()
	g.mu.Unlock()
	return doc.AccessToken, nil
}

// --- Session creation ---

// paypalLink is one entry of an order's HATEOAS links array.
type paypalLink struct {
	Href string `json:"href"`
	Rel  string `json:"rel"`
}

// approveURL picks the buyer's hosted approval page from the order's
// links. The v2 API has used both rel names ("approve" historically,
// "payer-action" in newer responses), so both are accepted.
func approveURL(links []paypalLink) string {
	for _, l := range links {
		if l.Rel == "approve" || l.Rel == "payer-action" {
			return l.Href
		}
	}
	return ""
}

// Create opens a PayPal order for the top-up: intent CAPTURE, one purchase
// unit carrying the amount, and the top-up's ID as custom_id — the key
// that rides through captures back to us (PayPal's counterpart of Stripe's
// client_reference_id). PayPal-Request-Id makes the create idempotent: a
// retried call returns the same order rather than a second charge.
func (g *PayPalGateway) Create(ctx context.Context, tu TopUp) (Session, error) {
	tok, err := g.accessToken(ctx)
	if err != nil {
		return Session{}, err
	}
	order := map[string]any{
		"intent": "CAPTURE",
		"purchase_units": []any{map[string]any{
			"custom_id": tu.ID,
			// PayPal amounts are decimal strings, not integers.
			"amount": map[string]any{
				"currency_code": strings.ToUpper(tu.Currency),
				"value":         minorUnitsToDecimal(tu.AmountMinor, tu.Currency),
			},
		}},
		"payment_source": map[string]any{
			"paypal": map[string]any{
				"experience_context": map[string]any{
					"user_action": "PAY_NOW",
					"return_url":  g.returnBase + "/payments/return?status=success",
					"cancel_url":  g.returnBase + "/payments/return?status=cancelled",
				},
			},
		},
	}
	payload, err := json.Marshal(order)
	if err != nil {
		return Session{}, fmt.Errorf("payments: encode paypal order: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.baseURL+"/v2/checkout/orders", bytes.NewReader(payload))
	if err != nil {
		return Session{}, fmt.Errorf("payments: build paypal request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("PayPal-Request-Id", tu.ID) // idempotent create

	resp, err := g.client.Do(req)
	if err != nil {
		return Session{}, fmt.Errorf("payments: paypal order request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Session{}, fmt.Errorf("payments: read paypal response: %w", err)
	}
	var doc struct {
		ID    string       `json:"id"`
		Links []paypalLink `json:"links"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return Session{}, fmt.Errorf("payments: paypal response is not JSON: %w", err)
	}
	if resp.StatusCode != http.StatusCreated || doc.Error != nil || doc.ID == "" {
		msg := "paypal error"
		if doc.Error != nil {
			msg = doc.Error.Message
		}
		return Session{}, fmt.Errorf("payments: paypal refused the order (http %d): %s", resp.StatusCode, msg)
	}
	payURL := approveURL(doc.Links)
	if payURL == "" {
		return Session{}, errors.New("payments: paypal order carries no approval link")
	}
	return Session{Reference: doc.ID, PaymentURL: payURL}, nil
}

// minorUnitsToDecimal renders gateway minor units as PayPal's decimal
// string. PayPal's currency exponents are 2 for the currencies the
// platform charges in (EUR et al.); JPY is the zero-decimal exception the
// pilot anticipates. An unknown currency assumes 2.
func minorUnitsToDecimal(minor int64, currency string) string {
	exp := 2
	if strings.EqualFold(currency, "jpy") {
		exp = 0
	}
	switch exp {
	case 0:
		return strconv.FormatInt(minor, 10)
	default:
		return fmt.Sprintf("%d.%02d", minor/100, minor%100)
	}
}

// ParseDecimalAmount parses PayPal's decimal amount string back into
// minor units — the inverse of minorUnitsToDecimal, applied to the
// capture's reported amount. More fraction digits than the currency's
// exponent is a malformed amount: refused, not truncated.
func ParseDecimalAmount(s, currency string) (int64, error) {
	exp := 2
	if strings.EqualFold(currency, "jpy") {
		exp = 0
	}
	intPart, fracPart, _ := strings.Cut(strings.TrimSpace(s), ".")
	if intPart == "" || len(intPart) > 15 {
		return 0, fmt.Errorf("payments: unreadable paypal amount %q", s)
	}
	whole, err := strconv.ParseInt(intPart, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("payments: unreadable paypal amount %q", s)
	}
	if len(fracPart) > exp {
		return 0, fmt.Errorf("payments: paypal amount %q carries more precision than %s allows", s, currency)
	}
	for len(fracPart) < exp {
		fracPart += "0"
	}
	var frac int64
	if fracPart != "" {
		frac, err = strconv.ParseInt(fracPart, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("payments: unreadable paypal amount %q", s)
		}
	}
	div := int64(1)
	for i := 0; i < exp; i++ {
		div *= 10
	}
	if whole > (1<<62)/div {
		return 0, fmt.Errorf("payments: paypal amount %q overflows the minor-unit range", s)
	}
	return whole*div + frac, nil
}

// --- Webhook verification & event translation ---

// PayPal outcomes we translate. CHECKOUT.ORDER.APPROVED is deliberately
// absent from the map: it arrives before the money moves, and its handler
// fires the capture (captureOrder) instead of settling.
//
//	PAID:       PAYMENT.CAPTURE.COMPLETED
//	FAILED:     PAYMENT.CAPTURE.DENIED, PAYMENT.CAPTURE.DECLINED
//	CANCELLED:  CHECKOUT.PAYMENT-APPROVAL.REVERSED
//
// Everything else (PENDING captures, REFUNDED, order lifecycle noise) is a
// verified event with nothing to do — answered as unhandled so the webhook
// 200s and PayPal stops retrying.
var paypalOutcome = map[string]Outcome{
	"PAYMENT.CAPTURE.COMPLETED":          OutcomePaid,
	"PAYMENT.CAPTURE.DENIED":             OutcomeFailed,
	"PAYMENT.CAPTURE.DECLINED":           OutcomeFailed,
	"CHECKOUT.PAYMENT-APPROVAL.REVERSED": OutcomeCancelled,
}

// transmissionTolerance bounds how fresh a delivery's timestamp must be —
// the Stripe five-minute window, applied to PAYPAL-TRANSMISSION-TIME.
// Genuine PayPal retries are re-signed at retransmission (a new
// transmission id and time each attempt), so the window blocks captured-
// and-replayed deliveries without bouncing late-but-legitimate retries.
const transmissionTolerance = 5 * time.Minute

// ParseCallback verifies the PayPal headers before parsing the event:
// algorithm pinned, timestamp fresh, certificate from a paypal.com URL,
// signature matched over "transmission_id|transmission_time|webhook_id|
// crc32" with rsa.VerifyPKCS1v15 (constant-time inside the stdlib). A
// CHECKOUT.ORDER.APPROVED additionally triggers the server-side capture —
// the money moves there, and the capture's own webhook settles.
func (g *PayPalGateway) ParseCallback(ctx context.Context, header http.Header, body []byte) (Callback, error) {
	if algo := header.Get("PAYPAL-AUTH-ALGO"); algo != "SHA256withRSA" {
		return Callback{}, fmt.Errorf("%w: unsupported auth algo %q", ErrCallbackRejected, algo)
	}
	transmissionID := header.Get("PAYPAL-TRANSMISSION-ID")
	transmissionTime := header.Get("PAYPAL-TRANSMISSION-TIME")
	signature := header.Get("PAYPAL-TRANSMISSION-SIG")
	certURL := header.Get("PAYPAL-CERT-URL")
	if transmissionID == "" || transmissionTime == "" || signature == "" || certURL == "" {
		return Callback{}, fmt.Errorf("%w: missing paypal webhook headers", ErrCallbackRejected)
	}
	stamp, err := time.Parse(time.RFC3339, transmissionTime)
	if err != nil {
		return Callback{}, fmt.Errorf("%w: unreadable transmission time", ErrCallbackRejected)
	}
	age := time.Since(stamp)
	if age < 0 {
		age = -age
	}
	if age > transmissionTolerance {
		return Callback{}, fmt.Errorf("%w: transmission outside the tolerance window", ErrCallbackRejected)
	}

	cert, err := g.certFor(ctx, certURL)
	if err != nil {
		return Callback{}, fmt.Errorf("%w: %v", ErrCallbackRejected, err)
	}

	// The signed message: unsigned decimal CRC32 (IEEE) of the raw body.
	signed := transmissionID + "|" + transmissionTime + "|" + g.webhookID + "|" +
		strconv.FormatUint(uint64(crc32.ChecksumIEEE(body)), 10)
	sig, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return Callback{}, fmt.Errorf("%w: signature is not base64", ErrCallbackRejected)
	}
	digest := sha256.Sum256([]byte(signed))
	if err := rsa.VerifyPKCS1v15(cert.PublicKey.(*rsa.PublicKey), crypto.SHA256, digest[:], sig); err != nil {
		return Callback{}, fmt.Errorf("%w: signature mismatch", ErrCallbackRejected)
	}

	// Verified. Now translate.
	var event struct {
		EventType string `json:"event_type"`
		Resource  struct {
			ID       string `json:"id"`
			Status   string `json:"status"`
			CustomID string `json:"custom_id"`
			Amount   *struct {
				Value    string `json:"value"`
				Currency string `json:"currency_code"`
			} `json:"amount"`
			SupplementaryData *struct {
				RelatedIDs *struct {
					OrderID string `json:"order_id"`
				} `json:"related_ids"`
			} `json:"supplementary_data"`
		} `json:"resource"`
	}
	if err := json.Unmarshal(body, &event); err != nil {
		return Callback{}, fmt.Errorf("%w: body is not a PayPal event", ErrCallbackRejected)
	}

	// CHECKOUT.ORDER.APPROVED: the buyer approved; PayPal holds no money
	// until we capture. Fire the capture and answer this delivery as
	// unhandled — settlement arrives via the capture's own webhook.
	if event.EventType == "CHECKOUT.ORDER.APPROVED" {
		if err := g.captureApprovedOrder(ctx, event.Resource.ID); err != nil {
			// A real error (not rejected, not unhandled): the API answers
			// 5xx and PayPal re-delivers APPROVED, re-attempting capture.
			return Callback{}, fmt.Errorf("payments: capture paypal order %s: %w", event.Resource.ID, err)
		}
		return Callback{}, errUnhandledEvent{eventType: event.EventType}
	}

	outcome, known := paypalOutcome[event.EventType]
	if !known {
		return Callback{}, errUnhandledEvent{eventType: event.EventType}
	}

	// Reference resolution: capture events name their order through
	// supplementary_data; the reversed-order event IS the order.
	reference := ""
	if event.Resource.SupplementaryData != nil && event.Resource.SupplementaryData.RelatedIDs != nil {
		reference = event.Resource.SupplementaryData.RelatedIDs.OrderID
	}
	if event.EventType == "CHECKOUT.PAYMENT-APPROVAL.REVERSED" {
		reference = event.Resource.ID
	}
	if reference == "" {
		return Callback{}, fmt.Errorf("%w: verified %s carries no order reference", ErrCallbackRejected, event.EventType)
	}

	amount := int64(0)
	if event.Resource.Amount != nil {
		amount, err = ParseDecimalAmount(event.Resource.Amount.Value, event.Resource.Amount.Currency)
		if err != nil {
			return Callback{}, err
		}
	}
	if outcome == OutcomePaid && amount <= 0 {
		return Callback{}, fmt.Errorf("%w: verified capture carries no amount", ErrCallbackRejected)
	}
	return Callback{Reference: reference, Outcome: outcome, AmountMinor: amount}, nil
}

// certFor fetches (and caches) the certificate a callback names. The URL
// passes the host policy first — an attacker-chosen certificate is the
// whole game in this scheme, so the default policy pins paypal.com.
func (g *PayPalGateway) certFor(ctx context.Context, certURL string) (*x509.Certificate, error) {
	if g.certHostOK != nil {
		if err := g.certHostOK(certURL); err != nil {
			return nil, fmt.Errorf("certificate url refused: %v", err)
		}
	}
	g.mu.Lock()
	if e, ok := g.certs[certURL]; ok && time.Since(e.at) < certCacheTTL {
		c := e.cert
		g.mu.Unlock()
		return c, nil
	}
	// Keep the cache from growing without bound under adversarial URLs.
	if len(g.certs) > 8 {
		g.certs = map[string]*certEntry{}
	}
	g.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, certURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build certificate request: %w", err)
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch certificate: %w", err)
	}
	defer resp.Body.Close()
	der, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch certificate (http %d)", resp.StatusCode)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("parse certificate: %w", err)
	}
	if _, ok := cert.PublicKey.(*rsa.PublicKey); !ok {
		return nil, errors.New("certificate carries no RSA public key")
	}
	g.mu.Lock()
	g.certs[certURL] = &certEntry{cert: cert, at: time.Now()}
	g.mu.Unlock()
	return cert, nil
}

// captureApprovedOrder moves the money on an approved order: mint a token,
// then POST the capture. A duplicate capture (APPROVED re-delivered after
// our first capture attempt succeeded but its response was lost) reads as
// success: the capture exists.
func (g *PayPalGateway) captureApprovedOrder(ctx context.Context, orderID string) error {
	tok, err := g.accessToken(ctx)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		g.baseURL+"/v2/checkout/orders/"+url.PathEscape(orderID)+"/capture", strings.NewReader("{}"))
	if err != nil {
		return fmt.Errorf("build capture request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.client.Do(req)
	if err != nil {
		return fmt.Errorf("capture request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == http.StatusCreated:
		return nil
	case resp.StatusCode == http.StatusUnprocessableEntity && bytes.Contains(body, []byte("ORDER_ALREADY_CAPTURED")):
		return nil
	default:
		return fmt.Errorf("capture refused (http %d)", resp.StatusCode)
	}
}
