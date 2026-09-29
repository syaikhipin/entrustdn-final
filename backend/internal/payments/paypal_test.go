package payments_test

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"errors"
	"hash/crc32"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/syaikhipin/entrustdn-final/backend/internal/payments"
)

// The real PayPal gateway, proven offline: signature verification runs the
// full RSA path against a test certificate served by the fake API, and
// order creation and capture ride the same fake. One fixture identity
// signs every "legitimate" delivery; attacker keys are minted separately
// in the rejection tests.

const paypalWebhookID = "2R269424P6803053B"

const orderIDFixture = "9P99943869582473S"

// paypalFixture is the whole offline PayPal: its key pair (the webhook
// signer and the certificate the fake API serves), the fake API server,
// and the capture counter.
type paypalFixture struct {
	gw       payments.Gateway
	keys     *paypalKeys
	certURL  string // where the fake API serves the certificate
	captures func() int
}

// paypalKeys is the fixture identity: the private key that signs webhook
// deliveries and the certificate the fake API serves for it.
type paypalKeys struct {
	key  *rsa.PrivateKey
	cert *x509.Certificate
}

func newPayPalKeys(t *testing.T) *paypalKeys {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "*.paypal.com"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	return &paypalKeys{key: key, cert: cert}
}

// signPayPal builds PAYPAL-TRANSMISSION-SIG the way PayPal does:
// base64(RSA-SHA256-PKCS1v15("id|time|webhook_id|crc32(body)")), the CRC
// unsigned decimal.
func (k *paypalKeys) signPayPal(transmissionID, transmissionTime, webhookID string, body []byte) string {
	signed := transmissionID + "|" + transmissionTime + "|" + webhookID + "|" +
		strconv.FormatUint(uint64(crc32.ChecksumIEEE(body)), 10)
	digest := sha256.Sum256([]byte(signed))
	sig, err := rsa.SignPKCS1v15(rand.Reader, k.key, crypto.SHA256, digest[:])
	if err != nil {
		panic(err) // fixture: a 2048-bit key cannot fail to sign
	}
	return base64.StdEncoding.EncodeToString(sig)
}

// paypalEvent marshals a webhook event body like PayPal sends.
func paypalEvent(t *testing.T, eventType, resourceID string, resource map[string]any) []byte {
	t.Helper()
	if resource == nil {
		resource = map[string]any{}
	}
	resource["id"] = resourceID
	body, err := json.Marshal(map[string]any{
		"id":            "WH-" + resourceID,
		"event_type":    eventType,
		"resource_type": "capture",
		"resource":      resource,
	})
	if err != nil {
		t.Fatalf("marshal paypal event: %v", err)
	}
	return body
}

// completedCapture is the resource shape of a settled capture: the amount
// PayPal actually took and the order it belongs to.
func completedCapture(amount string) map[string]any {
	return map[string]any{
		"status": "COMPLETED",
		"amount": map[string]any{"value": amount, "currency_code": "EUR"},
		"supplementary_data": map[string]any{
			"related_ids": map[string]any{"order_id": orderIDFixture},
		},
	}
}

// captureRef is a capture without money moving (denied, declined): no
// amount, but the order link is still how the callback finds its top-up.
func captureRef(status string) map[string]any {
	return map[string]any{
		"status": status,
		"supplementary_data": map[string]any{
			"related_ids": map[string]any{"order_id": orderIDFixture},
		},
	}
}

// signedDelivery wraps an event body with the PayPal headers and a valid
// signature by this identity at the given transmission time. certURL is
// passed through so tests can aim the fetch at the fake API — or
// somewhere hostile.
func (k *paypalKeys) signedDelivery(certURL string, body []byte, at time.Time) (http.Header, []byte) {
	transmissionID := "6e3b26a0-9287-11e7-ac1e-6b62a8a99ac4"
	stamp := at.UTC().Format(time.RFC3339)
	header := http.Header{}
	header.Set("PAYPAL-AUTH-ALGO", "SHA256withRSA")
	header.Set("PAYPAL-TRANSMISSION-ID", transmissionID)
	header.Set("PAYPAL-TRANSMISSION-TIME", stamp)
	header.Set("PAYPAL-CERT-URL", certURL)
	header.Set("PAYPAL-TRANSMISSION-SIG", k.signPayPal(transmissionID, stamp, paypalWebhookID, body))
	return header, body
}

// newPayPalFixture builds the gateway against a fake PayPal API serving
// the fixture identity's certificate. The gateway's certificate-host
// policy is replaced with one that accepts exactly the fake API's host —
// the same shape of pinning the production policy does for paypal.com.
func newPayPalFixture(t *testing.T) *paypalFixture {
	t.Helper()
	keys := newPayPalKeys(t)

	var mu sync.Mutex
	captures := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.URL.Path == "/v1/oauth2/token" && r.Method == http.MethodPost:
			if user, pass, ok := r.BasicAuth(); !ok || user != "client-id" || pass != "client-secret" {
				http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"tok_123","expires_in":32400}`))
		case r.URL.Path == "/v2/checkout/orders" && r.Method == http.MethodPost:
			var doc map[string]any
			if err := json.NewDecoder(r.Body).Decode(&doc); err != nil || doc["intent"] != "CAPTURE" {
				http.Error(w, `{"error":{"message":"bad order"}}`, http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"` + orderIDFixture + `","status":"CREATED","links":[` +
				`{"href":"https://www.paypal.com/checkoutnow?token=` + orderIDFixture + `","rel":"payer-action","method":"GET"}]}`))
		case strings.HasSuffix(r.URL.Path, "/capture") && r.Method == http.MethodPost:
			captures++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"CAPTURE-ID","status":"COMPLETED"}`))
		case r.URL.Path == "/v1/notifications/certs/CERT-360caa42":
			w.Header().Set("Content-Type", "application/x-x509-ca-cert")
			_, _ = w.Write(keys.cert.Raw)
		default:
			http.Error(w, `{"error":{"message":"no such route"}}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	gw, err := payments.NewPayPal(payments.Config{
		Provider:      "paypal",
		APIKey:        "client-id",
		WebhookSecret: "client-secret",
		WebhookID:     paypalWebhookID,
		Currency:      "eur",
		MicrosPerCent: 10_000,
		ReturnBaseURL: "https://thresh.example",
	})
	if err != nil {
		t.Fatalf("NewPayPal: %v", err)
	}
	g := gw.(*payments.PayPalGateway)
	g.SetBaseURL(srv.URL)
	// The fake API's certificates come from the test server's host, not
	// paypal.com — pin the policy to exactly that host, the same shape of
	// check the production policy applies to paypal.com.
	g.SetCertHostCheck(func(certURL string) error {
		if !strings.HasPrefix(certURL, srv.URL+"/") {
			return errors.New("certificate host is outside the fake API")
		}
		return nil
	})
	return &paypalFixture{
		gw:      g,
		keys:    keys,
		certURL: srv.URL + "/v1/notifications/certs/CERT-360caa42",
		captures: func() int {
			mu.Lock()
			defer mu.Unlock()
			return captures
		},
	}
}

func TestPayPalCreateOpensOrder(t *testing.T) {
	fx := newPayPalFixture(t)

	sess, err := fx.gw.Create(t.Context(), payments.TopUp{
		ID: "top_1", AmountMinor: 2500, Currency: "eur", Status: payments.StatusPending,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if sess.Reference != orderIDFixture {
		t.Errorf("reference = %q, want the fake order id", sess.Reference)
	}
	if sess.PaymentURL == "" {
		t.Error("payment url is empty")
	}
}

func TestPayPalCreateRequiresConfiguredIdentity(t *testing.T) {
	if _, err := payments.NewPayPal(payments.Config{Provider: "stripe"}); err == nil {
		t.Error("NewPayPal for provider stripe succeeded, want refusal")
	}
	if _, err := payments.NewPayPal(payments.Config{Provider: "paypal", APIKey: "id", WebhookSecret: "secret"}); err == nil {
		t.Error("NewPayPal without webhook id succeeded, want refusal")
	}
}

func TestPayPalVerifySignatureAcceptsFreshValid(t *testing.T) {
	fx := newPayPalFixture(t)
	body := paypalEvent(t, "PAYMENT.CAPTURE.COMPLETED", "CAP-1", completedCapture("25.00"))
	h, b := fx.keys.signedDelivery(fx.certURL, body, time.Now())

	cb, err := fx.gw.ParseCallback(t.Context(), h, b)
	if err != nil {
		t.Fatalf("ParseCallback: %v", err)
	}
	if cb.Reference != orderIDFixture || cb.Outcome != payments.OutcomePaid || cb.AmountMinor != 2500 {
		t.Errorf("callback = %+v, want order ref/paid/2500", cb)
	}
}

func TestPayPalVerifySignatureRejects(t *testing.T) {
	fx := newPayPalFixture(t)
	body := paypalEvent(t, "PAYMENT.CAPTURE.COMPLETED", "CAP-1", completedCapture("25.00"))
	attacker := newPayPalKeys(t)

	for _, tt := range []struct {
		name   string
		header func() http.Header
		body   []byte
	}{
		{
			name:   "missing headers",
			header: func() http.Header { return http.Header{} },
			body:   body,
		},
		{
			name: "wrong algorithm",
			header: func() http.Header {
				h, _ := fx.keys.signedDelivery(fx.certURL, body, time.Now())
				h.Set("PAYPAL-AUTH-ALGO", "SHA1withRSA")
				return h
			},
			body: body,
		},
		{
			name: "signed by a different key",
			header: func() http.Header {
				h, _ := attacker.signedDelivery(fx.certURL, body, time.Now())
				return h
			},
			body: body,
		},
		{
			name: "tampered body",
			header: func() http.Header {
				h, _ := fx.keys.signedDelivery(fx.certURL, body, time.Now())
				return h
			},
			body: paypalEvent(t, "PAYMENT.CAPTURE.COMPLETED", "CAP-1", completedCapture("99.00")),
		},
		{
			name: "stale transmission (replay past tolerance)",
			header: func() http.Header {
				h, _ := fx.keys.signedDelivery(fx.certURL, body, time.Now().Add(-10*time.Minute))
				return h
			},
			body: body,
		},
		{
			name: "certificate from outside the pinned host",
			header: func() http.Header {
				// Signed by the real key, but the certificate lives on a
				// host the policy refuses: the fetch never happens.
				h, _ := fx.keys.signedDelivery("https://evil.example/cert", body, time.Now())
				return h
			},
			body: body,
		},
		{
			name: "garbage signature",
			header: func() http.Header {
				h, _ := fx.keys.signedDelivery(fx.certURL, body, time.Now())
				h.Set("PAYPAL-TRANSMISSION-SIG", "not base64!!")
				return h
			},
			body: body,
		},
		{
			name: "garbage body",
			header: func() http.Header {
				h, _ := fx.keys.signedDelivery(fx.certURL, []byte("not json"), time.Now())
				return h
			},
			body: []byte("not json"),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			b := tt.body
			if b == nil {
				b = body
			}
			if _, err := fx.gw.ParseCallback(t.Context(), tt.header(), b); err == nil {
				t.Error("ParseCallback accepted an unverifiable callback")
			} else if !errors.Is(err, payments.ErrCallbackRejected) {
				t.Errorf("err = %v, want ErrCallbackRejected", err)
			}
		})
	}
}

func TestPayPalEventTranslation(t *testing.T) {
	fx := newPayPalFixture(t)

	for _, tt := range []struct {
		event       string
		resource    map[string]any
		wantOutcome payments.Outcome
	}{
		{"PAYMENT.CAPTURE.COMPLETED", completedCapture("25.00"), payments.OutcomePaid},
		{"PAYMENT.CAPTURE.DENIED", captureRef("DENIED"), payments.OutcomeFailed},
		{"PAYMENT.CAPTURE.DECLINED", captureRef("DECLINED"), payments.OutcomeFailed},
		{
			"CHECKOUT.PAYMENT-APPROVAL.REVERSED",
			map[string]any{"status": "REVERSED"},
			payments.OutcomeCancelled,
		},
	} {
		t.Run(tt.event, func(t *testing.T) {
			body := paypalEvent(t, tt.event, orderIDFixture, tt.resource)
			h, b := fx.keys.signedDelivery(fx.certURL, body, time.Now())
			cb, err := fx.gw.ParseCallback(t.Context(), h, b)
			if err != nil {
				t.Fatalf("ParseCallback(%s): %v", tt.event, err)
			}
			if cb.Outcome != tt.wantOutcome {
				t.Errorf("outcome = %q, want %q", cb.Outcome, tt.wantOutcome)
			}
			if cb.Reference != orderIDFixture {
				t.Errorf("reference = %q, want %q", cb.Reference, orderIDFixture)
			}
		})
	}

	// A verified but irrelevant event (refunds, pending captures) reports
	// IsUnhandledEvent so the webhook can 200 it.
	body := paypalEvent(t, "PAYMENT.CAPTURE.REFUNDED", "CAP-X", map[string]any{"status": "REFUNDED"})
	h, b := fx.keys.signedDelivery(fx.certURL, body, time.Now())
	_, err := fx.gw.ParseCallback(t.Context(), h, b)
	if err == nil {
		t.Fatal("unhandled event parsed as a callback, want errUnhandledEvent")
	}
	if !payments.IsUnhandledEvent(err) {
		t.Errorf("err = %v, want IsUnhandledEvent", err)
	}
}

func TestPayPalApprovedOrderTriggersCapture(t *testing.T) {
	fx := newPayPalFixture(t)

	body := paypalEvent(t, "CHECKOUT.ORDER.APPROVED", orderIDFixture, map[string]any{"status": "APPROVED"})
	h, b := fx.keys.signedDelivery(fx.certURL, body, time.Now())
	_, err := fx.gw.ParseCallback(t.Context(), h, b)
	if err == nil {
		t.Fatal("approved order parsed as a settlement, want errUnhandledEvent")
	}
	if !payments.IsUnhandledEvent(err) {
		t.Errorf("err = %v, want IsUnhandledEvent", err)
	}
	if got := fx.captures(); got != 1 {
		t.Errorf("capture endpoint hit %d times, want exactly 1", got)
	}

	// PayPal re-delivers APPROVED when the first delivery's response is
	// lost: we capture again, and the duplicate reads as success at
	// PayPal's end (ORDER_ALREADY_CAPTURED) rather than an error — the
	// delivery itself still answers as unhandled.
	h, b = fx.keys.signedDelivery(fx.certURL, body, time.Now())
	_, err = fx.gw.ParseCallback(t.Context(), h, b)
	if err == nil {
		t.Fatal("re-delivered APPROVED settled, want errUnhandledEvent again")
	}
	if !payments.IsUnhandledEvent(err) {
		t.Errorf("err = %v, want IsUnhandledEvent", err)
	}
	if got := fx.captures(); got != 2 {
		t.Errorf("capture endpoint hit %d times after re-delivery, want 2 (idempotent at PayPal, not here)", got)
	}
}

func TestPayPalAmountParsing(t *testing.T) {
	for _, tt := range []struct {
		decimal  string
		currency string
		want     int64
		wantErr  bool
	}{
		{"25.00", "EUR", 2500, false},
		{"0.99", "eur", 99, false},
		{"230", "USD", 23000, false},
		{"1234.5", "GBP", 123450, false},
		{"1234", "JPY", 1234, false},
		{"1.234", "EUR", 0, true},             // more precision than the exponent
		{"", "EUR", 0, true},                  // empty
		{"12x", "EUR", 0, true},               // not a number
		{"99999999999999999", "USD", 0, true}, // overflow guard
	} {
		got, err := payments.ParseDecimalAmount(tt.decimal, tt.currency)
		if tt.wantErr {
			if err == nil {
				t.Errorf("ParseDecimalAmount(%q, %s) = %d, want error", tt.decimal, tt.currency, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseDecimalAmount(%q, %s): %v", tt.decimal, tt.currency, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseDecimalAmount(%q, %s) = %d, want %d", tt.decimal, tt.currency, got, tt.want)
		}
	}
}
