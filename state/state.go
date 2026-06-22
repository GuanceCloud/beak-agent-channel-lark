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
	AccountID                  string            `json:"account_id"`
	AppID                      string            `json:"app_id,omitempty"`
	BaseURL                    string            `json:"base_url,omitempty"`
	Brand                      string            `json:"brand,omitempty"`
	TenantAccessToken          string            `json:"tenant_access_token,omitempty"`
	TokenExpiresAt             time.Time         `json:"tenant_access_token_expires_at,omitempty"`
	BotOpenID                  string            `json:"bot_open_id,omitempty"`
	BotName                    string            `json:"bot_name,omitempty"`
	BotUserID                  string            `json:"bot_user_id,omitempty"`
	BotUnionID                 string            `json:"bot_union_id,omitempty"`
	ChannelLinkSession         string            `json:"channel_link_session,omitempty"`
	PeerSessions               map[string]string `json:"peer_sessions,omitempty"`
	UserDisplayNames           map[string]string `json:"user_display_names,omitempty"`
	ChatDisplayNames           map[string]string `json:"chat_display_names,omitempty"`
	InboundSeen                map[string]string `json:"inbound_seen,omitempty"`
	SentBeakMessages           map[string]string `json:"sent_beak_messages,omitempty"`
	StreamCursors              map[string]string `json:"stream_cursors,omitempty"`
	StreamConnectionState      string            `json:"stream_connection_state,omitempty"`
	StreamConnectedAt          time.Time         `json:"stream_connected_at,omitempty"`
	StreamDisconnectedAt       time.Time         `json:"stream_disconnected_at,omitempty"`
	StreamLastActivityAt       time.Time         `json:"stream_last_activity_at,omitempty"`
	StreamLastPingAt           time.Time         `json:"stream_last_ping_at,omitempty"`
	StreamLastPongAt           time.Time         `json:"stream_last_pong_at,omitempty"`
	StreamLastEventAt          time.Time         `json:"stream_last_event_at,omitempty"`
	StreamLastError            string            `json:"stream_last_error,omitempty"`
	StreamLastErrorAt          time.Time         `json:"stream_last_error_at,omitempty"`
	StreamReconnectRequestedAt time.Time         `json:"stream_reconnect_requested_at,omitempty"`
	StreamReconnectError       string            `json:"stream_reconnect_error,omitempty"`
	StreamReconnectErrorAt     time.Time         `json:"stream_reconnect_error_at,omitempty"`
	StreamSessionExpired       bool              `json:"stream_session_expired,omitempty"`
	UpdatedAt                  time.Time         `json:"updated_at"`
}

func (a *AccountState) EnsureMaps() {
	if a == nil {
		return
	}
	if a.PeerSessions == nil {
		a.PeerSessions = make(map[string]string)
	}
	if a.UserDisplayNames == nil {
		a.UserDisplayNames = make(map[string]string)
	}
	if a.ChatDisplayNames == nil {
		a.ChatDisplayNames = make(map[string]string)
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
	pruneStringMap(account.UserDisplayNames)
	pruneStringMap(account.ChatDisplayNames)
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

func pruneStringMap(values map[string]string) {
	if len(values) <= maxTrackedStateKeys {
		return
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for len(values) > maxTrackedStateKeys && len(keys) > 0 {
		delete(values, keys[0])
		keys = keys[1:]
	}
}
