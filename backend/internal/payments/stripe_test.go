package payments_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/syaikhipin/entrustdn-final/backend/internal/payments"
)

// The real Stripe gateway, proven offline: callback verification is a pure
// function over the signature header, and the session creation rides a
// fake Stripe API. The secret is the fixture's — test001 in the official
// examples, mirrored here so the vectors stay recognizable.

const testSecret = "whsec_test"

// signStripeHeader builds a Stripe-Signature header the way Stripe does:
// HMAC-SHA256 over "{t}.{payload}", hex-encoded, in the t=…,v1=… scheme.
func signStripeHeader(t *testing.T, secret string, payload []byte, at time.Time) string {
	t.Helper()
	stamp := strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(stamp))
	mac.Write([]byte("."))
	mac.Write(payload)
	return "t=" + stamp + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

// stripeEvent marshals a checkout event body like Stripe sends.
func stripeEvent(t *testing.T, eventType, sessionID string, amount int64) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"type": eventType,
		"data": map[string]any{
			"object": map[string]any{
				"id":           sessionID,
				"amount_total": amount,
				"object":       "checkout.session",
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal stripe event: %v", err)
	}
	return body
}

// signedCallback wraps an event body with a valid signature header.
func signedCallback(t *testing.T, payload []byte, at time.Time) (http.Header, []byte) {
	t.Helper()
	header := http.Header{}
	header.Set("Stripe-Signature", signStripeHeader(t, testSecret, payload, at))
	return header, payload
}

// newStripeGateway returns the gateway pointed at a fake Stripe API.
func newStripeGateway(t *testing.T) (*httptest.Server, payments.Gateway) {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A minimal Checkout Session creation: echo an id and hosted URL,
		// reject requests that don't look like Stripe form posts.
		if r.URL.Path != "/v1/checkout/sessions" {
			http.Error(w, `{"error":{"message":"no such route"}}`, http.StatusNotFound)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, `{"error":{"message":"bad form"}}`, http.StatusBadRequest)
			return
		}
		if r.Form.Get("line_items[0][price_data][unit_amount]") == "" ||
			r.Form.Get("line_items[0][price_data][currency]") == "" {
			http.Error(w, `{"error":{"message":"unit_amount and currency required"}}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"cs_test_%d","url":"https://pay.example/cs_test_%d"}`, time.Now().UnixNano(), time.Now().UnixNano())
	}))
	t.Cleanup(srv.Close)

	gw, err := payments.NewStripe(payments.Config{
		Provider:      "stripe",
		APIKey:        "sk_test_1234",
		WebhookSecret: testSecret,
		ReturnBaseURL: "https://thresh.example",
	})
	if err != nil {
		t.Fatalf("NewStripe: %v", err)
	}
	stripeGW := gw.(*payments.StripeGateway)
	stripeGW.SetBaseURL(srv.URL)
	return srv, gw
}

func TestStripeCreateOpensCheckoutSession(t *testing.T) {
	_, gw := newStripeGateway(t)

	sess, err := gw.Create(t.Context(), payments.TopUp{
		ID: "top_1", AmountMinor: 2500, Currency: "eur", Status: payments.StatusPending,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !strings.HasPrefix(sess.Reference, "cs_test_") {
		t.Errorf("reference = %q, want a checkout session id", sess.Reference)
	}
	if sess.PaymentURL == "" {
		t.Error("payment url is empty")
	}
}

func TestStripeCreateSurfacesGatewayRefusals(t *testing.T) {
	_, gw := newStripeGateway(t)

	// Empty currency: the fake API (like the real one) refuses the form.
	_, err := gw.Create(t.Context(), payments.TopUp{
		ID: "top_bad", AmountMinor: 100, Currency: "", Status: payments.StatusPending,
	})
	if err == nil {
		t.Error("Create with empty currency succeeded, want the gateway refusal")
	}
}

func TestStripeVerifySignatureAcceptsFreshValid(t *testing.T) {
	gw, err := payments.NewStripe(payments.Config{Provider: "stripe", APIKey: "sk", WebhookSecret: testSecret})
	if err != nil {
		t.Fatalf("NewStripe: %v", err)
	}
	body := stripeEvent(t, "checkout.session.completed", "cs_ok", 1000)
	header, _ := signedCallback(t, body, time.Now())

	cb, err := gw.ParseCallback(t.Context(), header, body)
	if err != nil {
		t.Fatalf("ParseCallback: %v", err)
	}
	if cb.Reference != "cs_ok" || cb.Outcome != payments.OutcomePaid || cb.AmountMinor != 1000 {
		t.Errorf("callback = %+v, want cs_ok/paid/1000", cb)
	}
}

func TestStripeVerifySignatureRejects(t *testing.T) {
	gw, err := payments.NewStripe(payments.Config{Provider: "stripe", APIKey: "sk", WebhookSecret: testSecret})
	if err != nil {
		t.Fatalf("NewStripe: %v", err)
	}
	body := stripeEvent(t, "checkout.session.completed", "cs_ok", 1000)

	for _, tt := range []struct {
		name   string
		header func() http.Header
		body   []byte
	}{
		{
			name:   "missing header",
			header: func() http.Header { return http.Header{} },
			body:   body,
		},
		{
			name: "wrong secret",
			header: func() http.Header {
				h := http.Header{}
				h.Set("Stripe-Signature", signStripeHeader(t, "whsec_other", body, time.Now()))
				return h
			},
			body: body,
		},
		{
			name: "tampered body",
			header: func() http.Header {
				h, _ := signedCallback(t, body, time.Now())
				return h
			},
			body: stripeEvent(t, "checkout.session.completed", "cs_ok", 999999),
		},
		{
			name: "stale timestamp (replay past tolerance)",
			header: func() http.Header {
				h := http.Header{}
				h.Set("Stripe-Signature", signStripeHeader(t, testSecret, body, time.Now().Add(-10*time.Minute)))
				return h
			},
			body: body,
		},
		{
			name: "garbage header",
			header: func() http.Header {
				h := http.Header{}
				h.Set("Stripe-Signature", "not a real signature")
				return h
			},
			body: body,
		},
		{
			name: "garbage body",
			header: func() http.Header {
				h, _ := signedCallback(t, []byte("not json"), time.Now())
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
			if _, err := gw.ParseCallback(t.Context(), tt.header(), b); err == nil {
				t.Error("ParseCallback accepted an unverifiable callback")
			}
		})
	}
}

func TestStripeEventTranslation(t *testing.T) {
	gw, err := payments.NewStripe(payments.Config{Provider: "stripe", APIKey: "sk", WebhookSecret: testSecret})
	if err != nil {
		t.Fatalf("NewStripe: %v", err)
	}
	for _, tt := range []struct {
		event       string
		wantOutcome payments.Outcome
	}{
		{"checkout.session.completed", payments.OutcomePaid},
		{"checkout.session.async_payment_succeeded", payments.OutcomePaid},
		{"checkout.session.async_payment_failed", payments.OutcomeFailed},
		{"checkout.session.expired", payments.OutcomeCancelled},
	} {
		t.Run(tt.event, func(t *testing.T) {
			body := stripeEvent(t, tt.event, "cs_x", 500)
			header, _ := signedCallback(t, body, time.Now())
			cb, err := gw.ParseCallback(t.Context(), header, body)
			if err != nil {
				t.Fatalf("ParseCallback(%s): %v", tt.event, err)
			}
			if cb.Outcome != tt.wantOutcome {
				t.Errorf("outcome = %q, want %q", cb.Outcome, tt.wantOutcome)
			}
		})
	}

	// A verified but irrelevant event (e.g. payment_intent events we don't
	// key on) reports IsUnhandledEvent so the webhook can 200 it.
	body := stripeEvent(t, "charge.refunded", "ch_x", 500)
	header, _ := signedCallback(t, body, time.Now())
	_, err = gw.ParseCallback(t.Context(), header, body)
	if err == nil {
		t.Fatal("unhandled event parsed as a callback, want errUnhandledEvent")
	}
	if !payments.IsUnhandledEvent(err) {
		t.Errorf("err = %v, want IsUnhandledEvent", err)
	}
}
