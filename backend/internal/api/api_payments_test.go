package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/syaikhipin/entrustdn-final/backend/internal/agentclient"
	"github.com/syaikhipin/entrustdn-final/backend/internal/api"
	"github.com/syaikhipin/entrustdn-final/backend/internal/credits"
	"github.com/syaikhipin/entrustdn-final/backend/internal/mailsink"
	"github.com/syaikhipin/entrustdn-final/backend/internal/membership"
	"github.com/syaikhipin/entrustdn-final/backend/internal/payments"
)

// Payment gateway endpoints (ticket 09) at Seam 1: the admin's gateway
// configuration (masked credentials), the consumer's top-up flow, and the
// signature-verified webhook. The gateway is always a fake registered in
// the payments registry — the real Stripe gateway's signature scheme is
// proven in the payments package tests.

// fakeCallbackGateway speaks the same wire protocol the payments package
// test uses: a JSON body with an X-Fake-Signature header as its
// verification marker.
type fakeCallbackGateway struct {
	failCreate bool
}

func (f *fakeCallbackGateway) Provider() string { return "fakepay" }

func (f *fakeCallbackGateway) Create(_ context.Context, tu payments.TopUp) (payments.Session, error) {
	if f.failCreate {
		return payments.Session{}, errors.New("fakepay is down")
	}
	return payments.Session{Reference: "fp_" + tu.ID, PaymentURL: "https://pay.fakepay.example/" + tu.ID}, nil
}

type fakeCallbackDoc struct {
	Reference   string `json:"reference"`
	Outcome     string `json:"outcome"`
	AmountMinor int64  `json:"amount_minor"`
}

func (f *fakeCallbackGateway) ParseCallback(_ context.Context, header http.Header, body []byte) (payments.Callback, error) {
	if header.Get("X-Fake-Signature") != "ok" {
		return payments.Callback{}, payments.ErrCallbackRejected
	}
	var doc fakeCallbackDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return payments.Callback{}, payments.ErrCallbackRejected
	}
	return payments.Callback{Reference: doc.Reference, Outcome: payments.Outcome(doc.Outcome), AmountMinor: doc.AmountMinor}, nil
}

// newPaymentsServer boots the API with membership + ledger + payments
// stores all in memory and a "fakepay" gateway registered. Returns the
// server, admin token, mail buffer, and the ledger for balance assertions.
func newPaymentsServer(t *testing.T, gw payments.Gateway) (*httptest.Server, string, *bytes.Buffer, *credits.MemoryStore) {
	t.Helper()
	agent := fakeAgent(t, "0.1.0")
	memStore := membership.NewMemoryStore()
	if err := memStore.PublishTOS(t.Context(), membership.TOSVersion{Version: "1.0", Body: "Be good."}); err != nil {
		t.Fatalf("seed TOS: %v", err)
	}
	ledger := credits.NewMemoryStore()
	payStore := payments.NewMemoryStore(ledger)
	mail := &bytes.Buffer{}

	paySvc := payments.NewService(payStore, payStore.LoadConfig, registryWith(t, gw).GatewayFor)
	srv := httptest.NewServer(api.NewHandler(api.Deps{
		Agent:   agentclient.New(agent.URL),
		Version: "test-backend",
		Store:   memStore,
		Mail:    mailsink.NewLogSink(mail),
		Credits: &api.CreditsDeps{
			Store: ledger,
			Rules: func(context.Context) (credits.PricingRules, error) { return testPricingRules(), nil },
			SaveRules: func(context.Context, credits.PricingRules) error { return nil },
		},
		Payments: &api.PaymentsDeps{Service: paySvc, PublicBaseURL: "https://thresh.example"},
	}))
	t.Cleanup(srv.Close)

	provisionAdmin(t, memStore, "admin@thresh.dev")
	token := loginWith(t, srv, "admin@thresh.dev", "admin-pass-2026")
	return srv, token, mail, ledger
}

func registryWith(t *testing.T, gw payments.Gateway) *payments.Registry {
	t.Helper()
	reg := payments.NewRegistry()
	reg.Register("fakepay", func(payments.Config) (payments.Gateway, error) { return gw, nil })
	return reg
}

// enableGateway stores the gateway config through the admin API — exactly
// how production enables the gateway (the config row is the only source
// of truth; nothing is pre-seeded).
func enableGateway(t *testing.T, srv *httptest.Server, adminToken string) {
	t.Helper()
	code, doc := postWithToken(t, srv, "/api/v1/admin/payments/config", adminToken, map[string]any{
		"provider": "fakepay", "api_key": "fk_test", "webhook_secret": "whsec_fake",
		"currency": "eur", "micros_per_cent": 10_000,
	})
	if code != http.StatusOK {
		t.Fatalf("enable gateway: %d (doc: %v)", code, doc)
	}
}

// fakeCallbackBody signs a callback for the fake gateway.
func fakeCallbackBody(t *testing.T, ref string, outcome string, amount int64) (*http.Header, []byte) {
	t.Helper()
	body, err := json.Marshal(fakeCallbackDoc{Reference: ref, Outcome: outcome, AmountMinor: amount})
	if err != nil {
		t.Fatalf("marshal fake callback: %v", err)
	}
	h := http.Header{}
	h.Set("X-Fake-Signature", "ok")
	return &h, body
}

// postRaw posts a raw body with arbitrary headers — the webhook caller.
func postRaw(t *testing.T, srv *httptest.Server, path string, header http.Header, body []byte) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	var doc map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&doc)
	return resp.StatusCode, doc
}

func TestAdminConfiguresGatewayAndConsumersSeeIt(t *testing.T) {
	srv, adminToken, mail, _ := newPaymentsServer(t, &fakeCallbackGateway{})
	_, consumerToken := newConsumer(t, srv, mail, "payer@example.org")

	// Before configuration: the config reads as disabled and the consumer's
	// top-up attempt is refused with 409 — a platform state, not their error.
	code, doc := getWithToken(t, srv, "/api/v1/admin/payments/config", adminToken)
	if code != http.StatusOK || doc["enabled"] != false {
		t.Fatalf("default config = (%d, %v), want disabled", code, doc)
	}
	code, _ = postWithToken(t, srv, "/api/v1/me/topups", consumerToken, map[string]any{"amount_minor": 2500})
	if code != http.StatusConflict {
		t.Errorf("top-up while disabled = %d, want 409", code)
	}

	// Save the config; the response masks the credentials.
	enableGateway(t, srv, adminToken)
	code, doc = getWithToken(t, srv, "/api/v1/admin/payments/config", adminToken)
	if code != http.StatusOK || doc["enabled"] != true {
		t.Fatalf("saved config = (%d, %v), want enabled", code, doc)
	}
	for _, secret := range []string{"fk_test", "whsec_fake"} {
		if strings.Contains(docString(doc), secret) {
			t.Errorf("config response leaks the credential %q: %v", secret, doc)
		}
	}
	if doc["api_key_set"] != true || doc["webhook_secret_set"] != true {
		t.Errorf("masked config = %v, want both credentials reported set", doc)
	}

	// Non-admins never touch the config surface.
	code, _ = getWithToken(t, srv, "/api/v1/admin/payments/config", consumerToken)
	if code != http.StatusForbidden {
		t.Errorf("consumer reads config = %d, want 403", code)
	}
	code, _ = postWithToken(t, srv, "/api/v1/admin/payments/config", consumerToken, map[string]any{
		"provider": "fakepay", "api_key": "x", "webhook_secret": "y", "currency": "eur", "micros_per_cent": 1,
	})
	if code != http.StatusForbidden {
		t.Errorf("consumer writes config = %d, want 403", code)
	}
}

func docString(doc map[string]any) string {
	b, _ := json.Marshal(doc)
	return string(b)
}

func TestConsumerTopUpFlowCreditsTheLedger(t *testing.T) {
	srv, adminToken, mail, ledger := newPaymentsServer(t, &fakeCallbackGateway{})
	consumerID, consumerToken := newConsumer(t, srv, mail, "topping@example.org")
	enableGateway(t, srv, adminToken)

	// Initiate: the response carries the payment URL and the credit amount.
	code, doc := postWithToken(t, srv, "/api/v1/me/topups", consumerToken, map[string]any{"amount_minor": 2500})
	if code != http.StatusCreated {
		t.Fatalf("initiate = %d (doc: %v)", code, doc)
	}
	tu := doc["top_up"].(map[string]any)
	ref := tu["reference"].(string)
	if !strings.HasPrefix(ref, "fp_") {
		t.Errorf("reference = %q, want a fakepay reference", ref)
	}
	if got := int64(tu["credits_micros"].(float64)); got != 25*credits.MicrosPerCredit {
		t.Errorf("credits_micros = %d, want 25 credits at 1 EUR/credit", got)
	}
	if bal, _ := ledger.Balance(t.Context(), credits.AccountScope(consumerID)); bal != 0 {
		t.Errorf("balance after initiate = %d, want 0", bal)
	}

	// Settle: the webhook (signed) posts the top-up movement.
	header, body := fakeCallbackBody(t, ref, "paid", 2500)
	code, doc = postRaw(t, srv, "/api/v1/payments/callback/fakepay", *header, body)
	if code != http.StatusOK {
		t.Fatalf("callback = %d (doc: %v)", code, doc)
	}
	if bal, _ := ledger.Balance(t.Context(), credits.AccountScope(consumerID)); bal != 25*credits.MicrosPerCredit {
		t.Errorf("balance after settlement = %d, want 25 credits", bal)
	}
	treasury, _ := ledger.Balance(t.Context(), credits.Scope(credits.ScopeTreasury))
	if treasury != -25*credits.MicrosPerCredit {
		t.Errorf("treasury = %d, want −25 credits", treasury)
	}

	// The consumer's history shows the settled top-up, and their credits
	// view shows the movement the settlement posted.
	code, doc = getWithToken(t, srv, "/api/v1/me/topups", consumerToken)
	if code != http.StatusOK {
		t.Fatalf("history = %d", code)
	}
	tops := doc["top_ups"].([]any)
	if len(tops) != 1 || tops[0].(map[string]any)["status"] != "settled" {
		t.Errorf("history = %v, want one settled top-up", tops)
	}
	code, doc = getWithToken(t, srv, "/api/v1/me/credits", consumerToken)
	if code != http.StatusOK {
		t.Fatalf("credits view = %d", code)
	}
	movs := doc["movements"].([]any)
	if len(movs) != 1 || movs[0].(map[string]any)["kind"] != credits.KindTopUp {
		t.Errorf("credits movements = %v, want one top_up", movs)
	}
}

func TestWebhookRejectsForgedAndUnknownCallbacks(t *testing.T) {
	srv, adminToken, mail, ledger := newPaymentsServer(t, &fakeCallbackGateway{})
	consumerID, consumerToken := newConsumer(t, srv, mail, "forgery@example.org")
	enableGateway(t, srv, adminToken)
	code, doc := postWithToken(t, srv, "/api/v1/me/topups", consumerToken, map[string]any{"amount_minor": 1000})
	if code != http.StatusCreated {
		t.Fatalf("initiate: %d (doc: %v)", code, doc)
	}
	ref := doc["top_up"].(map[string]any)["reference"].(string)

	// Forged: right reference, no/bad signature marker.
	for _, sig := range []string{"", "forged"} {
		h := http.Header{}
		if sig != "" {
			h.Set("X-Fake-Signature", sig)
		}
		body, _ := json.Marshal(fakeCallbackDoc{Reference: ref, Outcome: "paid", AmountMinor: 1000})
		if code, _ := postRaw(t, srv, "/api/v1/payments/callback/fakepay", h, body); code != http.StatusBadRequest {
			t.Errorf("forged callback (sig=%q) = %d, want 400", sig, code)
		}
	}
	if bal, _ := ledger.Balance(t.Context(), credits.AccountScope(consumerID)); bal != 0 {
		t.Errorf("balance after forgery = %d, want 0", bal)
	}

	// Wrong provider in the URL: 404 before any verification.
	h, body := fakeCallbackBody(t, ref, "paid", 1000)
	if code, _ := postRaw(t, srv, "/api/v1/payments/callback/stripe", *h, body); code != http.StatusNotFound {
		t.Errorf("wrong provider = %d, want 404", code)
	}

	// Unknown reference: 404, nothing credited.
	h, body = fakeCallbackBody(t, "fp_unknown", "paid", 1000)
	if code, _ := postRaw(t, srv, "/api/v1/payments/callback/fakepay", *h, body); code != http.StatusNotFound {
		t.Errorf("unknown reference = %d, want 404", code)
	}

	// The real callback still settles after all the noise.
	h, body = fakeCallbackBody(t, ref, "paid", 1000)
	if code, _ := postRaw(t, srv, "/api/v1/payments/callback/fakepay", *h, body); code != http.StatusOK {
		t.Errorf("real callback after forgeries = %d, want 200", code)
	}
	if bal, _ := ledger.Balance(t.Context(), credits.AccountScope(consumerID)); bal != 10*credits.MicrosPerCredit {
		t.Errorf("balance = %d, want 10 credits", bal)
	}
}

func TestWebhookFailureCancelAndReplayPaths(t *testing.T) {
	srv, adminToken, mail, ledger := newPaymentsServer(t, &fakeCallbackGateway{})
	consumerID, consumerToken := newConsumer(t, srv, mail, "outcomes@example.org")
	enableGateway(t, srv, adminToken)

	initiate := func() string {
		code, doc := postWithToken(t, srv, "/api/v1/me/topups", consumerToken, map[string]any{"amount_minor": 500})
		if code != http.StatusCreated {
			t.Fatalf("initiate: %d (doc: %v)", code, doc)
		}
		return doc["top_up"].(map[string]any)["reference"].(string)
	}

	// Failure: 200 (the callback was handled), no credit.
	ref := initiate()
	h, body := fakeCallbackBody(t, ref, "failed", 500)
	if code, _ := postRaw(t, srv, "/api/v1/payments/callback/fakepay", *h, body); code != http.StatusOK {
		t.Errorf("failed callback = %d, want 200", code)
	}

	// Cancellation: same.
	ref = initiate()
	h, body = fakeCallbackBody(t, ref, "cancelled", 500)
	if code, _ := postRaw(t, srv, "/api/v1/payments/callback/fakepay", *h, body); code != http.StatusOK {
		t.Errorf("cancelled callback = %d, want 200", code)
	}

	// Success, then replays: exactly one credit across five deliveries.
	ref = initiate()
	h, body = fakeCallbackBody(t, ref, "paid", 500)
	if code, _ := postRaw(t, srv, "/api/v1/payments/callback/fakepay", *h, body); code != http.StatusOK {
		t.Fatalf("paid callback = %d, want 200", code)
	}
	for range 4 {
		if code, _ := postRaw(t, srv, "/api/v1/payments/callback/fakepay", *h, body); code != http.StatusOK {
			t.Errorf("replay = %d, want 200 (idempotent)", code)
		}
	}

	// Exactly one of the three top-ups credited: 5 micro-credits total —
	// the failure and cancellation never moved money.
	if bal, _ := ledger.Balance(t.Context(), credits.AccountScope(consumerID)); bal != 5*credits.MicrosPerCredit {
		t.Errorf("balance = %d, want 5 credits (one settled of three)", bal)
	}
}

func TestGatewayFailureLeavesNoTopUpRow(t *testing.T) {
	gw := &fakeCallbackGateway{failCreate: true}
	srv, adminToken, mail, _ := newPaymentsServer(t, gw)
	consumerToken := newConsumerSecond(t, srv, mail)
	enableGateway(t, srv, adminToken)

	code, _ := postWithToken(t, srv, "/api/v1/me/topups", consumerToken, map[string]any{"amount_minor": 2500})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("initiate with a down gateway = %d, want 422", code)
	}
	code, doc := getWithToken(t, srv, "/api/v1/me/topups", consumerToken)
	if code != http.StatusOK || len(doc["top_ups"].([]any)) != 0 {
		t.Errorf("history after failed initiate = (%d, %v), want empty", code, doc["top_ups"])
	}
}

// newConsumerSecond registers a second consumer on an existing server
// (the fake gateway is shared per server, so tests need distinct emails).
func newConsumerSecond(t *testing.T, srv *httptest.Server, mail *bytes.Buffer) string {
	t.Helper()
	_, token := newConsumer(t, srv, mail, "down-gateway@example.org")
	return token
}
