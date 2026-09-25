package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/syaikhipin/entrustdn-final/backend/internal/contract"
	"github.com/syaikhipin/entrustdn-final/backend/internal/conversations"
	"github.com/syaikhipin/entrustdn-final/backend/internal/roster"
)

// Member roster & conversation endpoints (ticket 11). The Farmer
// Organization keeps a roster of Member contact points — people, not
// accounts — and opens conversations the Agent carries over Channels
// (WhatsApp, Telegram, email). A conversation pauses for days and resumes
// mid-thread when the Member answers through their resumable link: the
// token in the link is the Member's whole capability, so the reply
// endpoint takes no session. Over-survey protection has the last word on
// both sides: stop words and the question cap end a conversation.

// RosterDeps carries the collaborators the roster endpoints need.
type RosterDeps struct {
	Store roster.Store
}

// ConversationsDeps carries the collaborators the conversation endpoints
// need. PublicBaseURL prefixes the Member's resumable links; empty means
// links still carry the token but render no URL.
type ConversationsDeps struct {
	Store         conversations.Store
	PublicBaseURL string
}

// ConversationClient is the slice of the agent client the conversation
// endpoints drive. *agentclient.Client satisfies it.
type ConversationClient interface {
	StartConversation(ctx context.Context, req contract.ConversationStartRequest) (contract.ConversationResponse, error)
	Converse(ctx context.Context, req contract.ConversationReplyRequest) (contract.ConversationResponse, error)
}

// registerRosterRoutes wires the Member roster endpoints when configured.
func (h *Handler) registerRosterRoutes(deps *RosterDeps) {
	h.roster = &rosterHandlers{svc: roster.NewService(deps.Store)}
	h.mux.HandleFunc("POST /api/v1/members", h.handleAddMember)
	h.mux.HandleFunc("GET /api/v1/members", h.handleListMembers)
	h.mux.HandleFunc("GET /api/v1/members/{id}", h.handleGetMember)
	h.mux.HandleFunc("PATCH /api/v1/members/{id}", h.handleUpdateMember)
	h.mux.HandleFunc("DELETE /api/v1/members/{id}", h.handleDeleteMember)
}

// registerConversationRoutes wires the conversation endpoints. It needs
// the roster (a conversation opens with a roster Member) and the Agent to
// speak conversation turns; otherwise the routes do not register.
func (h *Handler) registerConversationRoutes(deps *ConversationsDeps) {
	if h.roster == nil {
		return
	}
	cc, ok := h.agent.(ConversationClient)
	if !ok {
		return
	}
	h.conversations = &conversationsHandlers{
		svc: conversations.NewService(deps.Store, AgentConversationAdapter{Client: cc}, deps.PublicBaseURL),
	}
	h.mux.HandleFunc("POST /api/v1/conversations", h.handleStartConversation)
	h.mux.HandleFunc("GET /api/v1/conversations", h.handleListConversations)
	h.mux.HandleFunc("GET /api/v1/conversations/{id}", h.handleGetConversation)
	// The Member's reply: no session — the resumable token is the capability.
	h.mux.HandleFunc("POST /api/v1/member/reply", h.handleMemberReply)
	// The resumable link's redemption surface: the token shows the
	// Member their conversation so far.
	h.mux.HandleFunc("GET /api/v1/member/resume/{token}", h.handleMemberResume)
}

// AgentConversationAdapter fits the agent client to conversations.Agent:
// the backend speaks to the agent only through the contract. Exported
// because main (cmd/api) needs the same fit when it wires the collection
// service's re-ask opener to the agent client.
type AgentConversationAdapter struct {
	Client ConversationClient
}

func (a AgentConversationAdapter) Start(ctx context.Context, req contract.ConversationStartRequest) (contract.ConversationResponse, error) {
	return a.Client.StartConversation(ctx, req)
}

func (a AgentConversationAdapter) Reply(ctx context.Context, req contract.ConversationReplyRequest) (contract.ConversationResponse, error) {
	return a.Client.Converse(ctx, req)
}

// rosterHandlers holds the resolved collaborators for the roster routes.
type rosterHandlers struct {
	svc *roster.Service
}

// conversationsHandlers holds the resolved collaborators for the
// conversation routes.
type conversationsHandlers struct {
	svc *conversations.Service
}

// memberJSON renders one roster entry: a contact point, never an account.
func memberJSON(m roster.Member) map[string]any {
	return map[string]any{
		"id":           m.ID,
		"display_name": m.DisplayName,
		"contact":      m.Contact,
		"created_at":   m.CreatedAt.Format(timeFormat),
		"updated_at":   m.UpdatedAt.Format(timeFormat),
	}
}

// conversationJSON renders one conversation: the durable thread, the
// answers gathered, and the Member's resumable token (the org minted the
// link, so it may see the capability it handed out).
func conversationJSON(c conversations.Conversation) map[string]any {
	thread := make([]map[string]any, 0, len(c.Thread))
	for _, turn := range c.Thread {
		thread = append(thread, map[string]any{"role": turn.Role, "body": turn.Body})
	}
	return map[string]any{
		"id":           c.ID,
		"member_id":    c.MemberID,
		"member_name":  c.MemberName,
		"contact":      c.Contact,
		"topic":        c.Topic,
		"questions":    c.Questions,
		"resume_token": c.ResumeToken,
		"thread":       thread,
		"answers":      c.Answers,
		"status":       string(c.Status),
		"created_at":   c.CreatedAt.Format(timeFormat),
		"updated_at":   c.UpdatedAt.Format(timeFormat),
	}
}

type memberRequest struct {
	DisplayName string `json:"display_name"`
	Contact     string `json:"contact"`
}

// handleAddMember records one Member contact point for the org.
func (h *Handler) handleAddMember(w http.ResponseWriter, r *http.Request) {
	org, ok := h.requireOrg(w, r)
	if !ok {
		return
	}
	var req memberRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	member, err := h.roster.svc.Add(r.Context(), org.ID, roster.NewMember{
		DisplayName: req.DisplayName,
		Contact:     req.Contact,
	})
	if err != nil {
		apiError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"member": memberJSON(member)})
}

// handleListMembers serves the org's roster, oldest first.
func (h *Handler) handleListMembers(w http.ResponseWriter, r *http.Request) {
	org, ok := h.requireOrg(w, r)
	if !ok {
		return
	}
	members, err := h.roster.svc.List(r.Context(), org.ID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "failed to list the roster")
		return
	}
	docs := make([]map[string]any, 0, len(members))
	for _, m := range members {
		docs = append(docs, memberJSON(m))
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": docs})
}

// handleGetMember serves one roster entry — the org's own, or 404.
func (h *Handler) handleGetMember(w http.ResponseWriter, r *http.Request) {
	org, ok := h.requireOrg(w, r)
	if !ok {
		return
	}
	member, err := h.roster.svc.ByID(r.Context(), r.PathValue("id"), org.ID)
	switch {
	case errors.Is(err, roster.ErrNotFound), errors.Is(err, roster.ErrForbidden):
		// A stranger's member reads as 404: existence is not disclosed.
		apiError(w, http.StatusNotFound, "no such member")
	case err != nil:
		apiError(w, http.StatusInternalServerError, "failed to load the member")
	default:
		writeJSON(w, http.StatusOK, map[string]any{"member": memberJSON(member)})
	}
}

// handleUpdateMember rewrites one Member's name and contact point.
func (h *Handler) handleUpdateMember(w http.ResponseWriter, r *http.Request) {
	org, ok := h.requireOrg(w, r)
	if !ok {
		return
	}
	var req memberRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	member, err := h.roster.svc.Update(r.Context(), r.PathValue("id"), org.ID, roster.NewMember{
		DisplayName: req.DisplayName,
		Contact:     req.Contact,
	})
	switch {
	case errors.Is(err, roster.ErrNotFound), errors.Is(err, roster.ErrForbidden):
		apiError(w, http.StatusNotFound, "no such member")
	case err != nil:
		apiError(w, http.StatusBadRequest, err.Error())
	default:
		writeJSON(w, http.StatusOK, map[string]any{"member": memberJSON(member)})
	}
}

// handleDeleteMember removes one Member from the roster.
func (h *Handler) handleDeleteMember(w http.ResponseWriter, r *http.Request) {
	org, ok := h.requireOrg(w, r)
	if !ok {
		return
	}
	err := h.roster.svc.Remove(r.Context(), r.PathValue("id"), org.ID)
	switch {
	case errors.Is(err, roster.ErrNotFound), errors.Is(err, roster.ErrForbidden):
		apiError(w, http.StatusNotFound, "no such member")
	case err != nil:
		apiError(w, http.StatusInternalServerError, "failed to remove the member")
	default:
		writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
	}
}

type startConversationRequest struct {
	MemberID  string   `json:"member_id"`
	Topic     string   `json:"topic"`
	Questions []string `json:"questions"`
	// RequestID optionally ties the conversation to one Data Collection's
	// request (ticket 12): the collection's sync matches conversations by
	// request ID, so gathering conversations carry it.
	RequestID string `json:"request_id"`
}

// handleStartConversation opens a conversation with one roster Member:
// the agent asks the first question over the Member's Channel, then
// pauses on its checkpoint. The resumable link's token comes back with
// the conversation — the org hands it to the Member.
func (h *Handler) handleStartConversation(w http.ResponseWriter, r *http.Request) {
	org, ok := h.requireOrg(w, r)
	if !ok {
		return
	}
	var req startConversationRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if len(req.Questions) == 0 {
		apiError(w, http.StatusBadRequest, "a conversation needs at least one question")
		return
	}
	member, err := h.roster.svc.ByID(r.Context(), req.MemberID, org.ID)
	if err != nil {
		apiError(w, http.StatusNotFound, "no such member")
		return
	}
	conv, err := h.conversations.svc.Start(r.Context(), conversations.Start{
		OrgID:      org.ID,
		RequestID:  req.RequestID,
		MemberID:   member.ID,
		MemberName: member.DisplayName,
		Contact:    member.Contact,
		Topic:      req.Topic,
		Questions:  req.Questions,
	})
	if err != nil {
		apiError(w, http.StatusBadGateway, "the agent could not open the conversation")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"conversation": conversationJSON(conv)})
}

// handleListConversations serves the org's conversations.
func (h *Handler) handleListConversations(w http.ResponseWriter, r *http.Request) {
	org, ok := h.requireOrg(w, r)
	if !ok {
		return
	}
	list, err := h.conversations.svc.ByOrg(r.Context(), org.ID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "failed to list conversations")
		return
	}
	docs := make([]map[string]any, 0, len(list))
	for _, c := range list {
		docs = append(docs, conversationJSON(c))
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversations": docs})
}

// handleGetConversation serves one conversation — the org's status view.
func (h *Handler) handleGetConversation(w http.ResponseWriter, r *http.Request) {
	org, ok := h.requireOrg(w, r)
	if !ok {
		return
	}
	conv, err := h.conversations.svc.ByID(r.Context(), r.PathValue("id"), org.ID)
	switch {
	case errors.Is(err, conversations.ErrNotFound):
		// Existence is not disclosed to strangers.
		apiError(w, http.StatusNotFound, "no such conversation")
	case err != nil:
		apiError(w, http.StatusInternalServerError, "failed to load the conversation")
	default:
		writeJSON(w, http.StatusOK, map[string]any{"conversation": conversationJSON(conv)})
	}
}

// handleMemberResume redeems a resumable link: the token is the whole
// capability, so no session is required — the Member sees their
// conversation as it stands (questions asked, answers given). An unknown
// token reads as 404: existence is not disclosed.
func (h *Handler) handleMemberResume(w http.ResponseWriter, r *http.Request) {
	conv, err := h.conversations.svc.ByToken(r.Context(), r.PathValue("token"))
	switch {
	case errors.Is(err, conversations.ErrNotFound):
		apiError(w, http.StatusNotFound, "no such conversation")
	case err != nil:
		apiError(w, http.StatusInternalServerError, "failed to load the conversation")
	default:
		writeJSON(w, http.StatusOK, map[string]any{"conversation": conversationJSON(conv)})
	}
}

type memberReplyRequest struct {
	Token   string `json:"token"`
	Message string `json:"message"`
}

// handleMemberReply resumes a conversation by the resumable link's token
// — the Member's whole capability: no account, no login, no session. The
// reply (typed text or a transcribed voice note upstream) rides to the
// agent, which resumes mid-thread and delivers its next question on the
// Member's Channel. Stop words stop the conversation; a closed one takes
// nothing more.
func (h *Handler) handleMemberReply(w http.ResponseWriter, r *http.Request) {
	var req memberReplyRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		apiError(w, http.StatusBadRequest, "the reply message is empty")
		return
	}
	conv, err := h.conversations.svc.MemberReply(r.Context(), req.Token, req.Message)
	switch {
	case errors.Is(err, conversations.ErrNotFound):
		apiError(w, http.StatusNotFound, "no such conversation")
	case errors.Is(err, conversations.ErrClosed):
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":        "this conversation has ended",
			"conversation": conversationJSON(conv),
		})
	case errors.Is(err, conversations.ErrOverSurveyed):
		// The cap held: the reply was taken, the questions are over.
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":        err.Error(),
			"conversation": conversationJSON(conv),
		})
	case err != nil:
		apiError(w, http.StatusBadGateway, "the agent could not be reached for this reply")
	default:
		writeJSON(w, http.StatusOK, map[string]any{"conversation": conversationJSON(conv)})
	}
}
