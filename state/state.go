package state

import (
	"fmt"
	"sort"
	"time"
)

const (
	maxTrackedStateKeys = 4096
	trackedStateTTL     = 7 * 24 * time.Hour
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
	now := time.Now().UTC()
	pruneTimestampMap(account.InboundSeen, now)
	pruneTimestampMap(account.SentBeakMessages, now)
	account.UpdatedAt = now
	return nil
}

func pruneTimestampMap(values map[string]string, now time.Time) {
	for key, raw := range values {
		if ts, err := time.Parse(time.RFC3339Nano, raw); err == nil && now.Sub(ts) > trackedStateTTL {
			delete(values, key)
		}
	}
	if len(values) <= maxTrackedStateKeys {
		return
	}
	type item struct {
		key string
		at  time.Time
	}
	items := make([]item, 0, len(values))
	for key, raw := range values {
		ts, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			ts = time.Time{}
		}
		items = append(items, item{key: key, at: ts})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].at.Before(items[j].at)
	})
	for len(values) > maxTrackedStateKeys && len(items) > 0 {
		delete(values, items[0].key)
		items = items[1:]
	}
}
