package lark

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

const (
	DefaultBaseURL     = "https://open.feishu.cn"
	DefaultLarkBaseURL = "https://open.larksuite.com"

	ChatTypeDirect = "direct"
	ChatTypeGroup  = "group"

	EventTypeMessageReceive = "im.message.receive_v1"
)

type TokenResponse struct {
	Code              int    `json:"code"`
	Msg               string `json:"msg"`
	TenantAccessToken string `json:"tenant_access_token"`
	Expire            int    `json:"expire"`
}

type SendTextRequest struct {
	ReceiveID     string
	ReceiveIDType string
	Text          string
	UUID          string
}

type SendMessageRequest struct {
	ReceiveID     string
	ReceiveIDType string
	MsgType       string
	Content       string
	UUID          string
}

type ReplyMessageRequest struct {
	MessageID     string
	MsgType       string
	Content       string
	UUID          string
	ReplyInThread *bool
}

type SendTextResponse = SendMessageResponse

type SendMessageResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		MessageID string `json:"message_id"`
		ChatID    string `json:"chat_id"`
	} `json:"data"`
}

type Webhook struct {
	Type      string
	Token     string
	Challenge string
	Schema    string
	Header    EventHeader
	Event     MessageEvent
}

type EventHeader struct {
	EventID    string `json:"event_id"`
	EventType  string `json:"event_type"`
	AppID      string `json:"app_id"`
	Token      string `json:"token"`
	CreateTime string `json:"create_time"`
	TenantKey  string `json:"tenant_key"`
}

type MessageEvent struct {
	Sender  EventSender  `json:"sender"`
	Message EventMessage `json:"message"`
}

type EventSender struct {
	SenderID   SenderID `json:"sender_id"`
	SenderType string   `json:"sender_type"`
}

type SenderID struct {
	OpenID  string `json:"open_id"`
	UserID  string `json:"user_id"`
	UnionID string `json:"union_id"`
}

type EventMessage struct {
	MessageID   string    `json:"message_id"`
	RootID      string    `json:"root_id"`
	ParentID    string    `json:"parent_id"`
	ThreadID    string    `json:"thread_id"`
	ChatID      string    `json:"chat_id"`
	ChatType    string    `json:"chat_type"`
	MessageType string    `json:"message_type"`
	Content     string    `json:"content"`
	CreateTime  string    `json:"create_time"`
	Mentions    []Mention `json:"mentions"`
}

type Mention struct {
	Key  string   `json:"key"`
	ID   SenderID `json:"id"`
	Name string   `json:"name"`
}

type ChatIdentity struct {
	ChatType      string
	ChatID        string
	SenderID      string
	ReplyTargetID string
}

func ParseWebhook(data []byte) (*Webhook, error) {
	var hook Webhook
	if err := json.Unmarshal(data, &hook); err != nil {
		return nil, fmt.Errorf("decode lark webhook: %w", err)
	}
	return &hook, nil
}

func (h Webhook) IsURLVerification() bool {
	return h.Type == "url_verification" && h.Challenge != ""
}

func (h Webhook) EventType() string {
	if h.Header.EventType != "" {
		return h.Header.EventType
	}
	return h.Type
}

func (h Webhook) EventID() string {
	return h.Header.EventID
}

func (h Webhook) VerifyToken(expected string) bool {
	expected = strings.TrimSpace(expected)
	if expected == "" {
		return true
	}
	if h.Token != "" {
		return h.Token == expected
	}
	if h.Header.Token != "" {
		return h.Header.Token == expected
	}
	return false
}

func (m EventMessage) Text() string {
	if m.MessageType != "" && m.MessageType != "text" {
		return ""
	}
	var parsed struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(m.Content), &parsed); err == nil && parsed.Text != "" {
		return normalizeText(parsed.Text)
	}
	return normalizeText(m.Content)
}

func (m EventMessage) ChatIdentity(senderOpenID string) ChatIdentity {
	chatID := strings.TrimSpace(m.ChatID)
	senderID := strings.TrimSpace(senderOpenID)
	switch strings.TrimSpace(m.ChatType) {
	case "group":
		return ChatIdentity{ChatType: ChatTypeGroup, ChatID: chatID, SenderID: senderID, ReplyTargetID: chatID}
	case "p2p", "direct":
		if chatID == "" {
			chatID = senderID
		}
		return ChatIdentity{ChatType: ChatTypeDirect, ChatID: chatID, SenderID: senderID, ReplyTargetID: chatID}
	default:
		return ChatIdentity{ChatType: "", ChatID: chatID, SenderID: senderID, ReplyTargetID: chatID}
	}
}

func (c ChatIdentity) StateKey() string {
	if c.ChatType == ChatTypeGroup {
		return ChatTypeGroup + ":" + c.ChatID
	}
	return c.ChatID
}

func (h Webhook) DedupeKey(accountUUID string) string {
	accountUUID = strings.TrimSpace(accountUUID)
	if id := strings.TrimSpace(h.Event.Message.MessageID); id != "" {
		return accountUUID + ":message:" + id
	}
	if id := strings.TrimSpace(h.Header.EventID); id != "" {
		return accountUUID + ":event:" + id
	}
	return accountUUID + ":chat:" + h.Event.Message.ChatID + ":time:" + h.Event.Message.CreateTime
}

func ReceiveIDTypeForTarget(target string) string {
	target = strings.TrimSpace(target)
	if strings.HasPrefix(target, "oc_") {
		return "chat_id"
	}
	if strings.HasPrefix(target, "ou_") {
		return "open_id"
	}
	if strings.HasPrefix(target, "on_") {
		return "union_id"
	}
	if strings.HasPrefix(target, "ouser_") || strings.HasPrefix(target, "u_") {
		return "user_id"
	}
	return "chat_id"
}

var atTagRE = regexp.MustCompile(`(?i)<at\s+[^>]*>(.*?)</at>`)

func normalizeText(text string) string {
	text = atTagRE.ReplaceAllString(text, "$1")
	return strings.TrimSpace(text)
}
