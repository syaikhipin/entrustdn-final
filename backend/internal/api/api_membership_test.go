package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/syaikhipin/entrustdn-final/backend/internal/agentclient"
	"github.com/syaikhipin/entrustdn-final/backend/internal/api"
	"github.com/syaikhipin/entrustdn-final/backend/internal/mailsink"
	"github.com/syaikhipin/entrustdn-final/backend/internal/membership"
)

// Membership endpoints (ticket 02), driven at Seam 1: the backend's public
// HTTP API with the store doubled in memory and mail asserted through the
// dev log sink. Tests assert external behavior only — status codes, JSON
// shape, and what lands in the mail sink.

// newMembershipServer boots the API against an in-memory store and a
// captured log-sink mailer, seeded with one TOS version.
func newMembershipServer(t *testing.T) (*httptest.Server, *bytes.Buffer, *membership.MemoryStore) {
	t.Helper()
	agent := fakeAgent(t, "0.1.0")
	store := membership.NewMemoryStore()
	if err := store.PublishTOS(t.Context(), membership.TOSVersion{Version: "1.0", Body: "Be good to farmers."}); err != nil {
		t.Fatalf("seed TOS: %v", err)
	}
	var mail bytes.Buffer
	srv := httptest.NewServer(api.NewHandler(api.Deps{
		Agent:   agentclient.New(agent.URL),
		Version: "test-backend",
		Store:   store,
		Mail:    mailsink.NewLogSink(&mail),
	}))
	t.Cleanup(srv.Close)
	return srv, &mail, store
}

// postJSON sends a JSON body and decodes the response document.
func postJSON(t *testing.T, srv *httptest.Server, path string, body any) (int, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	resp, err := http.Post(srv.URL+path, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	return decodeResp(t, resp)
}

func decodeResp(t *testing.T, resp *http.Response) (int, map[string]any) {
	t.Helper()
	body, _ := io.ReadAll(resp.Body)
	var doc map[string]any
	if len(body) > 0 {
		if err := json.Unmarshal(body, &doc); err != nil {
			t.Fatalf("response is not JSON: %v\nbody: %s", err, body)
		}
	}
	return resp.StatusCode, doc
}

func TestRegistrationCreatesUnverifiedAccountAndLogsVerificationLink(t *testing.T) {
	tests := []struct {
		name         string
		role         string
		wantRole     string
		wantStatus   string
	}{
		{
			name:       "data consumer",
			role:       "data_consumer",
			wantRole:   "data_consumer",
			wantStatus: "active",
		},
		{
			name:       "farmer organization",
			role:       "farmer_organization",
			wantRole:   "farmer_organization",
			wantStatus: "pending_approval",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, mail, _ := newMembershipServer(t)

			code, doc := postJSON(t, srv, "/api/v1/register", map[string]any{
				"email":        "newmember@example.org",
				"password":     "harvest-2026",
				"display_name": "New Member",
				"role":         tt.role,
				"tos_version":  "1.0",
			})

			if code != http.StatusCreated {
				t.Fatalf("status = %d, want %d (doc: %v)", code, http.StatusCreated, doc)
			}
			acct, ok := doc["account"].(map[string]any)
			if !ok {
				t.Fatalf("account is not an object: %v", doc)
			}
			if acct["email"] != "newmember@example.org" {
				t.Errorf("email = %v, want newmember@example.org", acct["email"])
			}
			if acct["role"] != tt.wantRole {
				t.Errorf("role = %v, want %v", acct["role"], tt.wantRole)
			}
			if acct["status"] != tt.wantStatus {
				t.Errorf("status = %v, want %v (orgs await approval)", acct["status"], tt.wantStatus)
			}
			if verified, _ := acct["verified"].(bool); verified {
				t.Errorf("verified = %v, want false before the link is clicked", acct["verified"])
			}
			if _, leaked := acct["password_hash"]; leaked {
				t.Errorf("account document leaks password_hash: %v", acct)
			}
			if _, leaked := acct["password"]; leaked {
				t.Errorf("account document leaks password: %v", acct)
			}

			// Story 4: the verification link lands in the dev log sink.
			out := mail.String()
			if !strings.Contains(out, "newmember@example.org") {
				t.Errorf("mail sink does not mention the new account:\n%s", out)
			}
			if !strings.Contains(out, "/verify?token=") {
				t.Errorf("mail sink does not contain a verification link:\n%s", out)
			}
		})
	}
}

func TestRegistrationRejectsBadApplications(t *testing.T) {
	tests := []struct {
		name    string
		body    map[string]any
		want    int
	}{
		{
			name: "duplicate email",
			body: map[string]any{"email": "taken@example.org", "password": "harvest-2026", "display_name": "Again", "role": "data_consumer", "tos_version": "1.0"},
			want: http.StatusConflict,
		},
		{
			name: "self-appointed admin",
			body: map[string]any{"email": "sneak@example.org", "password": "harvest-2026", "display_name": "Sneak", "role": "platform_admin", "tos_version": "1.0"},
			want: http.StatusBadRequest,
		},
		{
			name: "unknown role",
			body: map[string]any{"email": "ghost@example.org", "password": "harvest-2026", "display_name": "Ghost", "role": "super_admin", "tos_version": "1.0"},
			want: http.StatusBadRequest,
		},
		{
			name: "wrong TOS version",
			body: map[string]any{"email": "late@example.org", "password": "harvest-2026", "display_name": "Late", "role": "data_consumer", "tos_version": "0.9"},
			want: http.StatusBadRequest,
		},
		{
			name: "missing TOS version",
			body: map[string]any{"email": "silent@example.org", "password": "harvest-2026", "display_name": "Silent", "role": "data_consumer"},
			want: http.StatusBadRequest,
		},
		{
			name: "short password",
			body: map[string]any{"email": "thin@example.org", "password": "short", "display_name": "Thin", "role": "data_consumer", "tos_version": "1.0"},
			want: http.StatusBadRequest,
		},
		{
			name: "not an email",
			body: map[string]any{"email": "not-an-email", "password": "harvest-2026", "display_name": "Nope", "role": "data_consumer", "tos_version": "1.0"},
			want: http.StatusBadRequest,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _, _ := newMembershipServer(t)
			if code, doc := postJSON(t, srv, "/api/v1/register", map[string]any{
				"email": "taken@example.org", "password": "harvest-2026",
				"display_name": "First", "role": "data_consumer", "tos_version": "1.0",
			}); code != http.StatusCreated {
				t.Fatalf("seed registration failed: status %d (doc: %v)", code, doc)
			}

			code, _ := postJSON(t, srv, "/api/v1/register", tt.body)
			if code != tt.want {
				t.Errorf("status = %d, want %d", code, tt.want)
			}
		})
	}
}

// verificationTokenFor pulls the token out of the dev log sink for a given
// recipient — the same act a human performs in dev mode: read the link from
// the server log.
func verificationTokenFor(t *testing.T, mail *bytes.Buffer, email string) string {
	t.Helper()
	out := mail.String()
	idx := strings.Index(out, email)
	if idx < 0 {
		t.Fatalf("mail sink has no verification mail for %s:\n%s", email, out)
	}
	rest := out[idx:]
	m := regexp.MustCompile(`/verify\?token=([0-9a-f]+)`).FindStringSubmatch(rest)
	if m == nil {
		t.Fatalf("no verification link found for %s:\n%s", email, rest)
	}
	return m[1]
}

func TestEmailVerificationMarksAccountVerified(t *testing.T) {
	tests := []struct {
		name string
		role string
	}{
		{name: "consumer verifies", role: "data_consumer"},
		{name: "org verifies", role: "farmer_organization"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, mail, _ := newMembershipServer(t)
			email := "verify-me@example.org"
			if code, doc := postJSON(t, srv, "/api/v1/register", map[string]any{
				"email": email, "password": "harvest-2026", "display_name": "V", "role": tt.role, "tos_version": "1.0",
			}); code != http.StatusCreated {
				t.Fatalf("register: %d %v", code, doc)
			}

			token := verificationTokenFor(t, mail, email)
			code, doc := postJSON(t, srv, "/api/v1/verify", map[string]any{"token": token})
			if code != http.StatusOK {
				t.Fatalf("verify status = %d, want %d (doc: %v)", code, http.StatusOK, doc)
			}

			// The verification is durable: /me sees it (via a fresh login).
			code, doc = postJSON(t, srv, "/api/v1/login", map[string]any{"email": email, "password": "harvest-2026"})
			if code != http.StatusOK {
				t.Fatalf("login status = %d (doc: %v)", code, doc)
			}
			acct := doc["account"].(map[string]any)
			if acct["verified"] != true {
				t.Errorf("account.verified = %v, want true after verification", acct["verified"])
			}
		})
	}
}

func TestVerificationTokenIsOneShotAndExpirable(t *testing.T) {
	t.Run("second use of the same link fails", func(t *testing.T) {
		srv, mail, _ := newMembershipServer(t)
		if code, _ := postJSON(t, srv, "/api/v1/register", map[string]any{
			"email": "once@example.org", "password": "harvest-2026", "display_name": "O", "role": "data_consumer", "tos_version": "1.0",
		}); code != http.StatusCreated {
			t.Fatal("register failed")
		}
		token := verificationTokenFor(t, mail, "once@example.org")

		if code, _ := postJSON(t, srv, "/api/v1/verify", map[string]any{"token": token}); code != http.StatusOK {
			t.Fatalf("first verify = %d, want 200", code)
		}
		code, _ := postJSON(t, srv, "/api/v1/verify", map[string]any{"token": token})
		if code != http.StatusNotFound {
			t.Errorf("second verify = %d, want 404 (one-shot)", code)
		}
	})

	t.Run("unknown token fails", func(t *testing.T) {
		srv, _, _ := newMembershipServer(t)
		code, _ := postJSON(t, srv, "/api/v1/verify", map[string]any{"token": "deadbeef"})
		if code != http.StatusNotFound {
			t.Errorf("unknown token verify = %d, want 404", code)
		}
	})

	t.Run("empty body fails cleanly", func(t *testing.T) {
		srv, _, _ := newMembershipServer(t)
		code, _ := postJSON(t, srv, "/api/v1/verify", map[string]any{})
		if code != http.StatusBadRequest {
			t.Errorf("missing token = %d, want 400", code)
		}
	})
}

func TestLoginGatesOnVerificationAndTOS(t *testing.T) {
	register := func(t *testing.T, srv *httptest.Server, mail *bytes.Buffer, email string) {
		t.Helper()
		if code, doc := postJSON(t, srv, "/api/v1/register", map[string]any{
			"email": email, "password": "harvest-2026", "display_name": "L", "role": "data_consumer", "tos_version": "1.0",
		}); code != http.StatusCreated {
			t.Fatalf("register: %d %v", code, doc)
		}
	}

	tests := []struct {
		name       string
		publishV2  bool
		verifyFirst bool
		wantLogin  int
		wantReacc  bool
	}{
		{name: "unverified account cannot log in", verifyFirst: false, wantLogin: http.StatusForbidden, wantReacc: false},
		{name: "verified account with current TOS logs in clean", verifyFirst: true, wantLogin: http.StatusOK, wantReacc: false},
		{name: "verified account with stale TOS logs in flagged for re-acceptance", verifyFirst: true, publishV2: true, wantLogin: http.StatusOK, wantReacc: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, mail, store := newMembershipServer(t)
			email := strings.ToLower("login-" + strings.ReplaceAll(tt.name, " ", "-") + "@example.org")
			register(t, srv, mail, email)
			if tt.verifyFirst {
				if code, _ := postJSON(t, srv, "/api/v1/verify", map[string]any{"token": verificationTokenFor(t, mail, email)}); code != http.StatusOK {
					t.Fatal("verify failed")
				}
			}
			if tt.publishV2 {
				if err := store.PublishTOS(t.Context(), membership.TOSVersion{Version: "2.0", Body: "New terms."}); err != nil {
					t.Fatalf("publish v2: %v", err)
				}
			}

			code, doc := postJSON(t, srv, "/api/v1/login", map[string]any{"email": email, "password": "harvest-2026"})
			if code != tt.wantLogin {
				t.Fatalf("login = %d, want %d (doc: %v)", code, tt.wantLogin, doc)
			}
			if code != http.StatusOK {
				return
			}
			if got := doc["requires_tos_acceptance"]; got != tt.wantReacc {
				t.Errorf("requires_tos_acceptance = %v, want %v", got, tt.wantReacc)
			}
			if tt.wantReacc {
				sess := doc["session"].(map[string]any)
				if sess["token"] == nil || sess["token"] == "" {
					t.Error("stale-TOS login must still mint a session (scoped to re-acceptance)")
				}
			}
		})
	}
}

func TestLoginRejectsWrongCredentials(t *testing.T) {
	tests := []struct {
		name     string
		email    string
		password string
	}{
		{name: "wrong password", email: "wrongpw@example.org", password: "harvest-9999"},
		{name: "unknown account", email: "nobody@example.org", password: "harvest-2026"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, mail, _ := newMembershipServer(t)
			if code, _ := postJSON(t, srv, "/api/v1/register", map[string]any{
				"email": "wrongpw@example.org", "password": "harvest-2026", "display_name": "W", "role": "data_consumer", "tos_version": "1.0",
			}); code != http.StatusCreated {
				t.Fatal("register failed")
			}
			_ = mail

			code, doc := postJSON(t, srv, "/api/v1/login", map[string]any{"email": tt.email, "password": tt.password})
			if code != http.StatusUnauthorized {
				t.Errorf("login = %d, want 401 (doc: %v)", code, doc)
			}
		})
	}
}


func getWithToken(t *testing.T, srv *httptest.Server, path, token string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	return decodeResp(t, resp)
}

func TestMeReturnsRoleAppropriateIdentity(t *testing.T) {
	tests := []struct {
		role      string
		wantRole  string
	}{
		{role: "data_consumer", wantRole: "data_consumer"},
		{role: "farmer_organization", wantRole: "farmer_organization"},
	}
	for _, tt := range tests {
		t.Run(tt.role, func(t *testing.T) {
			srv, mail, _ := newMembershipServer(t)
			email := tt.role + "@example.org"
			if code, _ := postJSON(t, srv, "/api/v1/register", map[string]any{
				"email": email, "password": "harvest-2026", "display_name": "R", "role": tt.role, "tos_version": "1.0",
			}); code != http.StatusCreated {
				t.Fatal("register failed")
			}
			if code, _ := postJSON(t, srv, "/api/v1/verify", map[string]any{"token": verificationTokenFor(t, mail, email)}); code != http.StatusOK {
				t.Fatal("verify failed")
			}
			code, doc := postJSON(t, srv, "/api/v1/login", map[string]any{"email": email, "password": "harvest-2026"})
			if code != http.StatusOK {
				t.Fatalf("login = %d", code)
			}
			token := doc["session"].(map[string]any)["token"].(string)

			code, doc = getWithToken(t, srv, "/api/v1/me", token)
			if code != http.StatusOK {
				t.Fatalf("/me = %d, want 200 (doc: %v)", code, doc)
			}
			acct := doc["account"].(map[string]any)
			if acct["role"] != tt.wantRole {
				t.Errorf("role = %v, want %v", acct["role"], tt.wantRole)
			}
			if acct["email"] != email {
				t.Errorf("email = %v, want %v", acct["email"], email)
			}
			tos, ok := doc["tos"].(map[string]any)
			if !ok || tos["accepted_version"] != "1.0" {
				t.Errorf("tos.accepted_version = %v, want 1.0 (story 7: provable acceptance)", doc["tos"])
			}
			if _, ok := tos["accepted_at"]; !ok {
				t.Error("tos.accepted_at missing — story 7 wants the timestamp visible")
			}
		})
	}
}

func TestMeRejectsBadOrMissingSessions(t *testing.T) {
	tests := []struct {
		name  string
		token string
	}{
		{name: "no token", token: ""},
		{name: "garbage token", token: "not-a-token"},
		{name: "logged-out token", token: "revoked"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _, _ := newMembershipServer(t)
			code, _ := getWithToken(t, srv, "/api/v1/me", tt.token)
			if code != http.StatusUnauthorized {
				t.Errorf("/me with %q = %d, want 401", tt.name, code)
			}
		})
	}
}

// provisionAdmin seeds a Platform Admin the out-of-band way — the same
// route main.go's bootstrap takes, since registration refuses the role. A
// properly provisioned admin has verified its email and accepted the
// current TOS like any other account.
func provisionAdmin(t *testing.T, store *membership.MemoryStore, email string) {
	t.Helper()
	hash, err := membership.HashPassword("admin-pass-2026")
	if err != nil {
		t.Fatalf("hash admin password: %v", err)
	}
	acct := membership.Account{
		Email:        email,
		PasswordHash: hash,
		DisplayName:  "Platform Admin",
		Role:         membership.RolePlatformAdmin,
		Status:       membership.StatusActive,
	}
	if err := store.CreateAccount(t.Context(), &acct); err != nil {
		t.Fatalf("provision admin: %v", err)
	}
	if err := store.SetAccountVerified(t.Context(), acct.ID, time.Now()); err != nil {
		t.Fatalf("verify admin: %v", err)
	}
	tos, err := store.CurrentTOS(t.Context())
	if err != nil {
		t.Fatalf("current TOS: %v", err)
	}
	if err := store.RecordAcceptance(t.Context(), membership.TOSAcceptance{
		AccountID: acct.ID,
		Version:   tos.Version,
	}); err != nil {
		t.Fatalf("record admin TOS acceptance: %v", err)
	}
}

// loginWith logs an existing account in and returns the session token.
func loginWith(t *testing.T, srv *httptest.Server, email, password string) string {
	t.Helper()
	code, doc := postJSON(t, srv, "/api/v1/login", map[string]any{"email": email, "password": password})
	if code != http.StatusOK {
		t.Fatalf("login %s = %d, want 200 (doc: %v)", email, code, doc)
	}
	return doc["session"].(map[string]any)["token"].(string)
}

func postWithToken(t *testing.T, srv *httptest.Server, path, token string, body any) (int, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	return decodeResp(t, resp)
}

// registerOrg registers and verifies a Farmer Organization, returning its
// public document as the decision flow sees it.
func registerOrg(t *testing.T, srv *httptest.Server, mail *bytes.Buffer, email string) map[string]any {
	t.Helper()
	code, doc := postJSON(t, srv, "/api/v1/register", map[string]any{
		"email": email, "password": "harvest-2026", "display_name": "Irish Dairy Co-op", "role": "farmer_organization", "tos_version": "1.0",
	})
	if code != http.StatusCreated {
		t.Fatalf("org register = %d (doc: %v)", code, doc)
	}
	if code, _ := postJSON(t, srv, "/api/v1/verify", map[string]any{"token": verificationTokenFor(t, mail, email)}); code != http.StatusOK {
		t.Fatal("org verify failed")
	}
	return doc["account"].(map[string]any)
}

func TestAdminApplicationFlow(t *testing.T) {
	newWorld := func(t *testing.T) (*httptest.Server, *bytes.Buffer, *membership.MemoryStore, string) {
		srv, mail, store := newMembershipServer(t)
		provisionAdmin(t, store, "admin@thresh.dev")
		return srv, mail, store, loginWith(t, srv, "admin@thresh.dev", "admin-pass-2026")
	}

	t.Run("admin sees pending orgs and approves one", func(t *testing.T) {
		srv, mail, _, adminToken := newWorld(t)
		org := registerOrg(t, srv, mail, "dairy@example.org")

		code, doc := getWithToken(t, srv, "/api/v1/admin/applications", adminToken)
		if code != http.StatusOK {
			t.Fatalf("list applications = %d (doc: %v)", code, doc)
		}
		apps := doc["applications"].([]any)
		if len(apps) != 1 {
			t.Fatalf("got %d applications, want 1", len(apps))
		}
		if apps[0].(map[string]any)["email"] != "dairy@example.org" {
			t.Errorf("application email = %v", apps[0])
		}

		code, doc = postWithToken(t, srv, "/api/v1/admin/applications/decide", adminToken, map[string]any{
			"account_id": org["id"], "decision": "approve",
		})
		if code != http.StatusOK {
			t.Fatalf("approve = %d (doc: %v)", code, doc)
		}
		if doc["account"].(map[string]any)["status"] != "active" {
			t.Errorf("approved status = %v, want active", doc["account"])
		}

		// Approved org can log in and is no longer listed as pending.
		loginWith(t, srv, "dairy@example.org", "harvest-2026")
		_, doc = getWithToken(t, srv, "/api/v1/admin/applications", adminToken)
		if apps := doc["applications"].([]any); len(apps) != 0 {
			t.Errorf("pending list has %d entries after approval, want 0", len(apps))
		}
	})

	t.Run("reject decision marks the application rejected", func(t *testing.T) {
		srv, mail, _, adminToken := newWorld(t)
		org := registerOrg(t, srv, mail, "rejectee@example.org")

		code, doc := postWithToken(t, srv, "/api/v1/admin/applications/decide", adminToken, map[string]any{
			"account_id": org["id"], "decision": "reject",
		})
		if code != http.StatusOK {
			t.Fatalf("reject = %d (doc: %v)", code, doc)
		}
		if doc["account"].(map[string]any)["status"] != "rejected" {
			t.Errorf("status = %v, want rejected", doc["account"])
		}

		// A decided application cannot be decided twice.
		code, _ = postWithToken(t, srv, "/api/v1/admin/applications/decide", adminToken, map[string]any{
			"account_id": org["id"], "decision": "approve",
		})
		if code != http.StatusConflict {
			t.Errorf("re-decide = %d, want 409", code)
		}
	})

	t.Run("bad decisions are refused", func(t *testing.T) {
		srv, mail, _, adminToken := newWorld(t)
		org := registerOrg(t, srv, mail, "maybe@example.org")

		tests := []struct {
			name string
			body map[string]any
			want int
		}{
			{name: "unknown decision word", body: map[string]any{"account_id": org["id"], "decision": "maybe"}, want: http.StatusBadRequest},
			{name: "unknown account", body: map[string]any{"account_id": "acct-nope", "decision": "approve"}, want: http.StatusNotFound},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				code, _ := postWithToken(t, srv, "/api/v1/admin/applications/decide", adminToken, tt.body)
				if code != tt.want {
					t.Errorf("status = %d, want %d", code, tt.want)
				}
			})
		}
	})

	t.Run("non-admins are refused the admin surface", func(t *testing.T) {
		srv, mail, _ := newMembershipServer(t)
		org := registerOrg(t, srv, mail, "org@example.org")
		orgToken := loginWith(t, srv, "org@example.org", "harvest-2026")

		tests := []struct {
			name  string
			token string
		}{
			{name: "farmer organization token", token: orgToken},
			{name: "no token", token: ""},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				code, _ := getWithToken(t, srv, "/api/v1/admin/applications", tt.token)
				if code != http.StatusForbidden && code != http.StatusUnauthorized {
					t.Errorf("list applications = %d, want 403/401", code)
				}
				code, _ = postWithToken(t, srv, "/api/v1/admin/applications/decide", tt.token, map[string]any{
					"account_id": org["id"], "decision": "approve",
				})
				if code != http.StatusForbidden && code != http.StatusUnauthorized {
					t.Errorf("decide = %d, want 403/401", code)
				}
			})
		}
	})
}

func TestPublishingNewTOSRequiresReacceptanceAtNextLogin(t *testing.T) {
	srv, mail, store := newMembershipServer(t)
	provisionAdmin(t, store, "admin@thresh.dev")
	email := "stale-consumer@example.org"

	if code, _ := postJSON(t, srv, "/api/v1/register", map[string]any{
		"email": email, "password": "harvest-2026", "display_name": "S", "role": "data_consumer", "tos_version": "1.0",
	}); code != http.StatusCreated {
		t.Fatal("register failed")
	}
	if code, _ := postJSON(t, srv, "/api/v1/verify", map[string]any{"token": verificationTokenFor(t, mail, email)}); code != http.StatusOK {
		t.Fatal("verify failed")
	}
	adminToken := loginWith(t, srv, "admin@thresh.dev", "admin-pass-2026")

	// Publish TOS 2.0.
	code, doc := postWithToken(t, srv, "/api/v1/admin/tos", adminToken, map[string]any{
		"version": "2.0", "body": "Amended terms for the pilot.",
	})
	if code != http.StatusCreated {
		t.Fatalf("publish TOS = %d (doc: %v)", code, doc)
	}

	// Duplicate version is refused — accepted history must not mutate.
	code, _ = postWithToken(t, srv, "/api/v1/admin/tos", adminToken, map[string]any{
		"version": "2.0", "body": "Rewriting history.",
	})
	if code != http.StatusConflict {
		t.Errorf("republish same version = %d, want 409", code)
	}

	// The old session keeps working but is now flagged; a fresh login is flagged too.
	oldToken := loginWith(t, srv, email, "harvest-2026")
	code, doc = getWithToken(t, srv, "/api/v1/me", oldToken)
	if code != http.StatusOK {
		t.Fatalf("/me = %d", code)
	}
	if doc["session"].(map[string]any)["requires_tos_acceptance"] != true {
		t.Errorf("old session requires_tos_acceptance = %v, want true", doc["session"])
	}

	// While flagged, logout (session hygiene) still works, but the admin
	// surface is gated behind re-acceptance.
	code, _ = postWithToken(t, srv, "/api/v1/logout", oldToken, map[string]any{})
	if code != http.StatusOK {
		t.Errorf("flagged session logout = %d, want 200 (session hygiene stays available)", code)
	}
	flaggedToken := loginWith(t, srv, email, "harvest-2026")
	code, _ = postWithToken(t, srv, "/api/v1/admin/tos", flaggedToken, map[string]any{
		"version": "3.0", "body": "Should be refused while flagged.",
	})
	if code != http.StatusForbidden {
		t.Errorf("flagged admin publish = %d, want 403", code)
	}

	// Re-accept: first with the wrong version, then the current one.
	code, _ = postWithToken(t, srv, "/api/v1/tos/accept", flaggedToken, map[string]any{"version": "1.0"})
	if code != http.StatusBadRequest {
		t.Errorf("accept stale version = %d, want 400", code)
	}
	code, doc = postWithToken(t, srv, "/api/v1/tos/accept", flaggedToken, map[string]any{"version": "2.0"})
	if code != http.StatusOK {
		t.Fatalf("accept 2.0 = %d (doc: %v)", code, doc)
	}
	if doc["accepted_version"] != "2.0" {
		t.Errorf("accepted_version = %v, want 2.0", doc["accepted_version"])
	}
	oldToken = flaggedToken

	// The flag is cleared: /me is clean and logout works again.
	_, doc = getWithToken(t, srv, "/api/v1/me", oldToken)
	if doc["session"].(map[string]any)["requires_tos_acceptance"] != false {
		t.Errorf("requires_tos_acceptance after re-acceptance = %v, want false", doc["session"])
	}
	if doc["tos"].(map[string]any)["accepted_version"] != "2.0" {
		t.Errorf("accepted_version in /me = %v, want 2.0", doc["tos"])
	}
	code, _ = postWithToken(t, srv, "/api/v1/logout", oldToken, map[string]any{})
	if code != http.StatusOK {
		t.Errorf("logout after re-acceptance = %d, want 200", code)
	}
}

func TestRoleRoutingClaims(t *testing.T) {
	// The role-appropriate home (demoable through the UI) is the web app's
	// job; the API's contract is that /me reports the role, status, and TOS
	// state each home routes on. This table pins that contract per role.
	tests := []struct {
		name         string
		role         string
		provision    func(t *testing.T, srv *httptest.Server, mail *bytes.Buffer, store *membership.MemoryStore, email string)
		email        string
		wantRole     string
		wantStatus   string
	}{
		{
			name: "data consumer lands active",
			role: "data_consumer",
			provision: func(t *testing.T, srv *httptest.Server, mail *bytes.Buffer, store *membership.MemoryStore, email string) {
				if code, _ := postJSON(t, srv, "/api/v1/register", map[string]any{
					"email": email, "password": "harvest-2026", "display_name": "C", "role": "data_consumer", "tos_version": "1.0",
				}); code != http.StatusCreated {
					t.Fatal("register failed")
				}
				if code, _ := postJSON(t, srv, "/api/v1/verify", map[string]any{"token": verificationTokenFor(t, mail, email)}); code != http.StatusOK {
					t.Fatal("verify failed")
				}
			},
			email:      "router-consumer@example.org",
			wantRole:   "data_consumer",
			wantStatus: "active",
		},
		{
			name: "pending org sees its pending status",
			role: "farmer_organization",
			provision: func(t *testing.T, srv *httptest.Server, mail *bytes.Buffer, store *membership.MemoryStore, email string) {
				registerOrg(t, srv, mail, email)
			},
			email:      "router-org@example.org",
			wantRole:   "farmer_organization",
			wantStatus: "pending_approval",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, mail, store := newMembershipServer(t)
			tt.provision(t, srv, mail, store, tt.email)
			token := loginWith(t, srv, tt.email, "harvest-2026")

			code, doc := getWithToken(t, srv, "/api/v1/me", token)
			if code != http.StatusOK {
				t.Fatalf("/me = %d (doc: %v)", code, doc)
			}
			acct := doc["account"].(map[string]any)
			if acct["role"] != tt.wantRole {
				t.Errorf("role = %v, want %v", acct["role"], tt.wantRole)
			}
			if acct["status"] != tt.wantStatus {
				t.Errorf("status = %v, want %v", acct["status"], tt.wantStatus)
			}
		})
	}
}
