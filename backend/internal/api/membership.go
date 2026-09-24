package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/syaikhipin/entrustdn-final/backend/internal/membership"
)

// Membership endpoints (ticket 02): registration, email verification, login
// with TOS gating, sessions, and the Platform Admin's application and TOS
// flows. All handlers talk to the membership.Store seam; verification links
// ride the mailsink (dev mode logs them — no mail server at pilot).

// verificationTTL is how long an emailed verification link stays valid.
const verificationTTL = 48 * time.Hour

// minPasswordLen keeps trivially guessable credentials out of the platform.
const minPasswordLen = 8

var emailPattern = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// apiError writes a JSON error document with a status code.
func apiError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// decodeJSONBody reads a JSON request body, rejecting malformed input.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		apiError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

type registerRequest struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	TOSVersion  string `json:"tos_version"`
}

// handleRegister creates an unverified account. Registrants must accept the
// current TOS version at sign-up (stories 1–3); self-appointed Platform
// Admins are refused — admins are provisioned, not registered.
func (h *Handler) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))

	role := membership.Role(req.Role)
	if role != membership.RoleDataConsumer && role != membership.RoleFarmerOrganization {
		apiError(w, http.StatusBadRequest, "role must be data_consumer or farmer_organization")
		return
	}
	if !emailPattern.MatchString(req.Email) {
		apiError(w, http.StatusBadRequest, "email is not a valid address")
		return
	}
	if len(req.Password) < minPasswordLen {
		apiError(w, http.StatusBadRequest, fmt.Sprintf("password must be at least %d characters", minPasswordLen))
		return
	}
	if strings.TrimSpace(req.DisplayName) == "" {
		apiError(w, http.StatusBadRequest, "display_name is required")
		return
	}

	// The accepted TOS version is recorded with the account (story 2).
	ctx := r.Context()
	tos, err := h.store.CurrentTOS(ctx)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "no Terms of Service published")
		return
	}
	if req.TOSVersion != tos.Version {
		apiError(w, http.StatusBadRequest, fmt.Sprintf("you must accept the current Terms of Service (version %s)", tos.Version))
		return
	}

	hash, err := membership.HashPassword(req.Password)
	if err != nil {
		apiError(w, http.StatusBadRequest, err.Error())
		return
	}

	acct := membership.Account{
		Email:        req.Email,
		PasswordHash: hash,
		DisplayName:  strings.TrimSpace(req.DisplayName),
		Role:         role,
	}
	acct.Status = membership.StatusActive
	if role == membership.RoleFarmerOrganization {
		// Story 6: only genuine organizations trade; applications await
		// a Platform Admin decision.
		acct.Status = membership.StatusPendingApproval
	}
	if err := h.store.CreateAccount(ctx, &acct); err != nil {
		if errors.Is(err, membership.ErrConflict) {
			apiError(w, http.StatusConflict, "an account with this email already exists")
			return
		}
		apiError(w, http.StatusInternalServerError, "failed to create account")
		return
	}

	// Record the accepted version per account — the proof story 7 asks for.
	if err := h.store.RecordAcceptance(ctx, membership.TOSAcceptance{
		AccountID: acct.ID,
		Version:   tos.Version,
	}); err != nil {
		apiError(w, http.StatusInternalServerError, "failed to record Terms of Service acceptance")
		return
	}

	// Story 4: verification links land in the dev log sink.
	if err := h.sendVerificationLink(ctx, req.Email); err != nil {
		// The account exists; surface the mail failure in the log and keep
		// the 201 — the link can be re-sent.
		log.Printf("api: failed to send verification mail to %s: %v", req.Email, err)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"account": acct.Public()})
}

// sendVerificationLink creates a one-shot token and hands the link to the
// mail sink. The token itself never appears in any API response.
func (h *Handler) sendVerificationLink(ctx context.Context, email string) error {
	acct, err := h.store.AccountByEmail(ctx, email)
	if err != nil {
		return fmt.Errorf("failed to load account for verification: %w", err)
	}
	token := newNonce() + newNonce() // 32 hex chars
	expires := time.Now().Add(verificationTTL)
	if err := h.store.CreateVerificationToken(ctx, token, acct.ID, expires); err != nil {
		return fmt.Errorf("failed to store verification token: %w", err)
	}
	link := "/verify?token=" + token
	if err := h.mail.SendVerification(email, link); err != nil {
		return fmt.Errorf("failed to hand verification link to the mail sink: %w", err)
	}
	return nil
}

type verifyRequest struct {
	Token string `json:"token"`
}

// handleVerify consumes a one-shot emailed token and marks the account
// verified. Unknown, used, or expired tokens all look the same: 404.
func (h *Handler) handleVerify(w http.ResponseWriter, r *http.Request) {
	var req verifyRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Token) == "" {
		apiError(w, http.StatusBadRequest, "token is required")
		return
	}

	ctx := r.Context()
	accountID, err := h.store.ConsumeVerificationToken(ctx, req.Token, time.Now())
	if err != nil {
		apiError(w, http.StatusNotFound, "this verification link is unknown, used, or expired")
		return
	}
	if err := h.store.SetAccountVerified(ctx, accountID, time.Now()); err != nil {
		apiError(w, http.StatusInternalServerError, "failed to mark the account verified")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"verified": true})
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// handleLogin checks credentials, refuses unverified accounts, and mints a
// bearer session. When a newer TOS version exists than the account last
// accepted (story 8), the session is flagged: it may act only on the TOS
// re-acceptance flow until re-acceptance clears it.
func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))

	ctx := r.Context()
	acct, err := h.store.AccountByEmail(ctx, email)
	if err != nil {
		// Same answer for unknown email and wrong password: 401, no hint
		// about which half was wrong — and the same work, so the endpoint
		// can't be used to enumerate registered addresses by timing.
		membership.DummyCheck(req.Password)
		apiError(w, http.StatusUnauthorized, "email or password is incorrect")
		return
	}
	if !membership.CheckPassword(acct.PasswordHash, req.Password) {
		apiError(w, http.StatusUnauthorized, "email or password is incorrect")
		return
	}
	if acct.VerifiedAt == nil {
		apiError(w, http.StatusForbidden, "verify your email address before logging in")
		return
	}

	stale, err := h.tosStaleFor(ctx, acct.ID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "failed to check Terms of Service state")
		return
	}

	token := newNonce() + newNonce()
	if err := h.store.CreateSession(ctx, membership.Session{
		Token:                token,
		AccountID:            acct.ID,
		RequiresReacceptance: stale,
	}); err != nil {
		apiError(w, http.StatusInternalServerError, "failed to create session")
		return
	}

	writeJSON(w, http.StatusOK, loginResponse(acct, token, stale))
}

// tosStaleFor reports whether the account's latest acceptance is older than
// the current TOS version, or it never accepted at all.
func (h *Handler) tosStaleFor(ctx context.Context, accountID string) (bool, error) {
	current, err := h.store.CurrentTOS(ctx)
	if err != nil {
		return false, fmt.Errorf("load current TOS: %w", err)
	}
	latest, err := h.store.LatestAcceptance(ctx, accountID)
	if err != nil {
		if errors.Is(err, membership.ErrNotFound) {
			return true, nil
		}
		return false, fmt.Errorf("load latest acceptance: %w", err)
	}
	return latest.Version != current.Version, nil
}

func loginResponse(acct membership.Account, token string, stale bool) map[string]any {
	return map[string]any{
		"account":                 acct.Public(),
		"session":                 map[string]any{"token": token},
		"requires_tos_acceptance": stale,
	}
}

// sessionAuth resolves the bearer token to a session + account. It writes
// the error response and returns false when unauthorized.
func (h *Handler) sessionAuth(w http.ResponseWriter, r *http.Request) (membership.Session, membership.Account, bool) {
	header := r.Header.Get("Authorization")
	token := strings.TrimPrefix(header, "Bearer ")
	if token == "" || token == header {
		apiError(w, http.StatusUnauthorized, "missing bearer session")
		return membership.Session{}, membership.Account{}, false
	}
	ctx := r.Context()
	sess, err := h.store.SessionByToken(ctx, token)
	if err != nil {
		apiError(w, http.StatusUnauthorized, "unknown or expired session")
		return membership.Session{}, membership.Account{}, false
	}
	acct, err := h.store.AccountByID(ctx, sess.AccountID)
	if err != nil {
		apiError(w, http.StatusUnauthorized, "unknown or expired session")
		return membership.Session{}, membership.Account{}, false
	}
	return sess, acct, true
}

// handleMe returns the caller's identity plus their TOS acceptance record —
// the payload the web app routes on (story 7's provable acceptance included).
func (h *Handler) handleMe(w http.ResponseWriter, r *http.Request) {
	sess, acct, ok := h.sessionAuth(w, r)
	if !ok {
		return
	}
	doc := map[string]any{
		"account": acct.Public(),
		"session": map[string]any{"requires_tos_acceptance": sess.RequiresReacceptance},
	}
	if latest, err := h.store.LatestAcceptance(r.Context(), acct.ID); err == nil {
		doc["tos"] = map[string]any{
			"accepted_version": latest.Version,
			"accepted_at":      latest.AcceptedAt,
		}
	}
	writeJSON(w, http.StatusOK, doc)
}

type acceptTOSRequest struct {
	Version string `json:"version"`
}

// handleTOSAccept records acceptance of the current TOS version and clears
// the re-acceptance flag on the caller's session (story 8's exit ramp).
func (h *Handler) handleTOSAccept(w http.ResponseWriter, r *http.Request) {
	var req acceptTOSRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	sess, acct, ok := h.sessionAuth(w, r)
	if !ok {
		return
	}

	ctx := r.Context()
	current, err := h.store.CurrentTOS(ctx)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "no Terms of Service published")
		return
	}
	if req.Version != "" && req.Version != current.Version {
		apiError(w, http.StatusBadRequest, fmt.Sprintf("version %s is not current; accept version %s", req.Version, current.Version))
		return
	}

	if err := h.store.RecordAcceptance(ctx, membership.TOSAcceptance{
		AccountID: acct.ID,
		Version:   current.Version,
	}); err != nil {
		apiError(w, http.StatusInternalServerError, "failed to record acceptance")
		return
	}
	sess.RequiresReacceptance = false
	if err := h.store.UpdateSession(ctx, sess); err != nil {
		apiError(w, http.StatusInternalServerError, "failed to update session")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"accepted_version":        current.Version,
		"requires_tos_acceptance": false,
	})
}

// handleCurrentTOS serves the current version's text for display at
// registration and re-acceptance.
func (h *Handler) handleCurrentTOS(w http.ResponseWriter, r *http.Request) {
	tos, err := h.store.CurrentTOS(r.Context())
	if err != nil {
		apiError(w, http.StatusNotFound, "no Terms of Service published")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version": tos.Version,
		"body":    tos.Body,
	})
}

func writeJSON(w http.ResponseWriter, code int, doc any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(doc)
}

// handleLogout deletes the caller's session. Logout is session hygiene, not
// a platform capability, so it stays available even to TOS-flagged sessions.
func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	sess, _, ok := h.sessionAuth(w, r)
	if !ok {
		return
	}
	if err := h.store.DeleteSession(r.Context(), sess.Token); err != nil {
		apiError(w, http.StatusInternalServerError, "failed to end session")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"logged_out": true})
}

// requireAdmin resolves the caller, refuses non-admins, and refuses admins
// whose session is still flagged for TOS re-acceptance (story 8: contract
// changes gate the surface the contract governs). It writes the error
// response and returns false when the caller may not proceed.
func (h *Handler) requireAdmin(w http.ResponseWriter, r *http.Request) (membership.Account, bool) {
	sess, acct, ok := h.sessionAuth(w, r)
	if !ok {
		return membership.Account{}, false
	}
	if sess.RequiresReacceptance {
		apiError(w, http.StatusForbidden, "accept the current Terms of Service first (see /api/v1/tos/accept)")
		return membership.Account{}, false
	}
	if acct.Role != membership.RolePlatformAdmin {
		apiError(w, http.StatusForbidden, "this endpoint is for Platform Admins")
		return membership.Account{}, false
	}
	return acct, true
}

// callerIsAdmin reports whether the request's session belongs to a
// Platform Admin, without writing an error response — for handlers where
// admin is one permitted caller among several (e.g. module deprecation,
// where the author is the other). Authentication failures still write.
func (h *Handler) callerIsAdmin(w http.ResponseWriter, r *http.Request) bool {
	_, acct, ok := h.sessionAuth(w, r)
	if !ok {
		return false
	}
	return acct.Role == membership.RolePlatformAdmin
}

// requireConsumer resolves the caller and refuses anyone but an approved
// Data Consumer (the same contract-gates-the-surface rule requireAdmin and
// requireOrg apply).
func (h *Handler) requireConsumer(w http.ResponseWriter, r *http.Request) (membership.Account, bool) {
	sess, acct, ok := h.sessionAuth(w, r)
	if !ok {
		return membership.Account{}, false
	}
	if sess.RequiresReacceptance {
		apiError(w, http.StatusForbidden, "accept the current Terms of Service first (see /api/v1/tos/accept)")
		return membership.Account{}, false
	}
	if acct.Role != membership.RoleDataConsumer {
		apiError(w, http.StatusForbidden, "this endpoint is for Data Consumers")
		return membership.Account{}, false
	}
	if acct.Status != membership.StatusActive {
		apiError(w, http.StatusForbidden, "your account is not active")
		return membership.Account{}, false
	}
	return acct, true
}

// handleListApplications lists Farmer Organization accounts awaiting a
// Platform Admin decision (story 6).
func (h *Handler) handleListApplications(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireAdmin(w, r); !ok {
		return
	}
	orgs, err := h.store.PendingOrganizations(r.Context())
	if err != nil {
		apiError(w, http.StatusInternalServerError, "failed to list applications")
		return
	}
	public := make([]membership.PublicAccount, 0, len(orgs))
	for _, org := range orgs {
		public = append(public, org.Public())
	}
	writeJSON(w, http.StatusOK, map[string]any{"applications": public})
}

type decideRequest struct {
	AccountID string `json:"account_id"`
	Decision  string `json:"decision"` // "approve" | "reject"
}

// handleApplicationDecision approves or rejects one Farmer Organization
// application. Decisions are terminal for rejected applications in v1.
func (h *Handler) handleApplicationDecision(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireAdmin(w, r); !ok {
		return
	}
	var req decideRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}

	var status membership.Status
	switch req.Decision {
	case "approve":
		status = membership.StatusActive
	case "reject":
		status = membership.StatusRejected
	default:
		apiError(w, http.StatusBadRequest, `decision must be "approve" or "reject"`)
		return
	}

	ctx := r.Context()
	acct, err := h.store.AccountByID(ctx, req.AccountID)
	if err != nil {
		apiError(w, http.StatusNotFound, "no such account")
		return
	}
	if acct.Role != membership.RoleFarmerOrganization {
		apiError(w, http.StatusBadRequest, "only Farmer Organization accounts go through application review")
		return
	}
	if acct.Status != membership.StatusPendingApproval {
		apiError(w, http.StatusConflict, "this application was already decided")
		return
	}
	if err := h.store.SetApplicationStatus(ctx, req.AccountID, status); err != nil {
		apiError(w, http.StatusInternalServerError, "failed to record the decision")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"account": membership.PublicAccount{
			ID:          acct.ID,
			Email:       acct.Email,
			DisplayName: acct.DisplayName,
			Role:        acct.Role,
			Status:      status,
			Verified:    acct.VerifiedAt != nil,
			CreatedAt:   acct.CreatedAt,
		},
	})
}

type publishTOSRequest struct {
	Version string `json:"version"`
	Body    string `json:"body"`
}

// handlePublishTOS publishes a new TOS version (story 8). Every account
// whose latest acceptance is older becomes stale at its next login.
func (h *Handler) handlePublishTOS(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireAdmin(w, r); !ok {
		return
	}
	var req publishTOSRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Version) == "" {
		apiError(w, http.StatusBadRequest, "version is required")
		return
	}
	if strings.TrimSpace(req.Body) == "" {
		apiError(w, http.StatusBadRequest, "body is required")
		return
	}

	ctx := r.Context()
	// Refuse silently-duplicate versions: republishing an existing number
	// would rewrite history accounts accepted against.
	if _, err := h.store.CurrentTOS(ctx); err == nil {
		versions, err := h.store.TOSVersions(ctx)
		if err != nil {
			apiError(w, http.StatusInternalServerError, "failed to check TOS history")
			return
		}
		for _, v := range versions {
			if v.Version == req.Version {
				apiError(w, http.StatusConflict, fmt.Sprintf("version %s already exists; publish a new version instead", req.Version))
				return
			}
		}
	}

	if err := h.store.PublishTOS(ctx, membership.TOSVersion{Version: req.Version, Body: req.Body}); err != nil {
		apiError(w, http.StatusInternalServerError, "failed to publish TOS version")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"version": req.Version, "published": true})
}
