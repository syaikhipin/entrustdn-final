package agentclient

import (
	"context"
	"fmt"
	"time"

	"github.com/syaikhipin/entrustdn-final/backend/internal/contract"
)

// Conversation round trips (ticket 11): the backend opens Member
// conversations and delivers Member replies over the contract; the agent
// answers with a conversation.response that names the conversation and
// acknowledges Channel delivery. An undelivered or misaddressed response
// is refused here, before the backend ever records the turn — a reply is
// only ever reported as sent once the medium has it.

// StartConversation opens a conversation with one Farmer Member: the agent
// asks the first question over the Member's Channel, then pauses on a
// checkpoint until the Member replies (ADR 0001's pause-for-days).
func (c *Client) StartConversation(ctx context.Context, req contract.ConversationStartRequest) (contract.ConversationResponse, error) {
	env, err := contract.NewConversationStartRequest(req, time.Now().UTC())
	if err != nil {
		return contract.ConversationResponse{}, fmt.Errorf("failed to build conversation start: %w", err)
	}
	return c.conversationTurn(ctx, env, req.ConversationID)
}

// Converse delivers one Member reply and gets the agent's next turn: the
// conversation resumes mid-thread from the agent's checkpoint.
func (c *Client) Converse(ctx context.Context, req contract.ConversationReplyRequest) (contract.ConversationResponse, error) {
	env, err := contract.NewConversationReplyRequest(req, time.Now().UTC())
	if err != nil {
		return contract.ConversationResponse{}, fmt.Errorf("failed to build conversation reply: %w", err)
	}
	return c.conversationTurn(ctx, env, req.ConversationID)
}

// conversationTurn sends one conversation envelope and validates the
// conversation.response: well-formed, addressed to the conversation that
// was asked about, and actually delivered on the medium.
func (c *Client) conversationTurn(ctx context.Context, env contract.Envelope, conversationID string) (contract.ConversationResponse, error) {
	replyEnv, err := c.roundTrip(ctx, env, contract.TypeConversationResponse, "conversation")
	if err != nil {
		return contract.ConversationResponse{}, err
	}

	var reply contract.ConversationResponse
	if err := replyEnv.PayloadInto(&reply); err != nil {
		return contract.ConversationResponse{}, fmt.Errorf("agent response payload invalid: %w", err)
	}
	if err := reply.Validate(); err != nil {
		return contract.ConversationResponse{}, fmt.Errorf("agent response invalid: %w", err)
	}
	if reply.ConversationID != conversationID {
		return reply, fmt.Errorf("agent answered conversation %q, want %q", reply.ConversationID, conversationID)
	}
	if !reply.Delivered {
		return reply, fmt.Errorf("agent did not deliver its message on %q: the medium refused the turn", conversationID)
	}
	return reply, nil
}
