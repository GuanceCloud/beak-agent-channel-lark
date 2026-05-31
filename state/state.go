package state

import (
	"fmt"
	"time"
)

type AccountState struct {
	AccountID          string            `json:"account_id"`
	AppID              string            `json:"app_id,omitempty"`
	BaseURL            string            `json:"base_url,omitempty"`
	Brand              string            `json:"brand,omitempty"`
	TenantAccessToken  string            `json:"tenant_access_token,omitempty"`
	TokenExpiresAt     time.Time         `json:"tenant_access_token_expires_at,omitempty"`
	BotOpenID          string            `json:"bot_open_id,omitempty"`
	BotUserID          string            `json:"bot_user_id,omitempty"`
	BotUnionID         string            `json:"bot_union_id,omitempty"`
	ChannelLinkSession string            `json:"channel_link_session,omitempty"`
	PeerSessions       map[string]string `json:"peer_sessions,omitempty"`
	InboundSeen        map[string]string `json:"inbound_seen,omitempty"`
	SentBeakMessages   map[string]string `json:"sent_beak_messages,omitempty"`
	StreamCursors      map[string]string `json:"stream_cursors,omitempty"`
	UpdatedAt          time.Time         `json:"updated_at"`
}

func (a *AccountState) EnsureMaps() {
	if a == nil {
		return
	}
	if a.PeerSessions == nil {
		a.PeerSessions = make(map[string]string)
	}
	if a.InboundSeen == nil {
		a.InboundSeen = make(map[string]string)
	}
	if a.SentBeakMessages == nil {
		a.SentBeakMessages = make(map[string]string)
	}
	if a.StreamCursors == nil {
		a.StreamCursors = make(map[string]string)
	}
}

func TouchAccount(account *AccountState) error {
	if account == nil {
		return fmt.Errorf("account state is nil")
	}
	if account.AccountID == "" {
		return fmt.Errorf("account_id is required")
	}
	account.EnsureMaps()
	account.UpdatedAt = time.Now().UTC()
	return nil
}
