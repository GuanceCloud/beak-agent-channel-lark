package lark

import (
	"bytes"
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

type BotInfoResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Bot  struct {
		ActivateStatus int      `json:"activate_status"`
		AppName        string   `json:"app_name"`
		AvatarURL      string   `json:"avatar_url"`
		IPWhiteList    []string `json:"ip_white_list"`
		OpenID         string   `json:"open_id"`
	} `json:"bot"`
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

type AddMessageReactionRequest struct {
	MessageID string
	EmojiType string
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

type MessageReactionResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		ReactionID string `json:"reaction_id"`
		Operator   struct {
			OperatorID   string `json:"operator_id"`
			OperatorType string `json:"operator_type"`
		} `json:"operator"`
		ActionTime   string `json:"action_time"`
		ReactionType struct {
			EmojiType string `json:"emoji_type"`
		} `json:"reaction_type"`
	} `json:"data"`
}

type MessageResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		Items   []MessageItem `json:"items"`
		Message MessageItem   `json:"message"`
	} `json:"data"`
}

func (r MessageResponse) FirstMessage() *MessageItem {
	if len(r.Data.Items) > 0 {
		return &r.Data.Items[0]
	}
	if strings.TrimSpace(r.Data.Message.MessageID) != "" {
		return &r.Data.Message
	}
	return nil
}

type MessageItem struct {
	MessageID      string      `json:"message_id"`
	RootID         string      `json:"root_id"`
	ParentID       string      `json:"parent_id"`
	ThreadID       string      `json:"thread_id"`
	ChatID         string      `json:"chat_id"`
	ChatType       string      `json:"chat_type"`
	MessageType    string      `json:"msg_type"`
	MessageTypeAlt string      `json:"message_type"`
	Content        string      `json:"content"`
	CreateTime     string      `json:"create_time"`
	Sender         EventSender `json:"sender"`
	Mentions       []Mention   `json:"mentions"`
}

func (m *MessageItem) UnmarshalJSON(data []byte) error {
	var raw struct {
		MessageID      string          `json:"message_id"`
		RootID         string          `json:"root_id"`
		ParentID       string          `json:"parent_id"`
		ThreadID       string          `json:"thread_id"`
		ChatID         string          `json:"chat_id"`
		ChatType       string          `json:"chat_type"`
		MessageType    string          `json:"msg_type"`
		MessageTypeAlt string          `json:"message_type"`
		Content        json.RawMessage `json:"content"`
		CreateTime     string          `json:"create_time"`
		Sender         EventSender     `json:"sender"`
		Mentions       []Mention       `json:"mentions"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*m = MessageItem{
		MessageID:      raw.MessageID,
		RootID:         raw.RootID,
		ParentID:       raw.ParentID,
		ThreadID:       raw.ThreadID,
		ChatID:         raw.ChatID,
		ChatType:       raw.ChatType,
		MessageType:    raw.MessageType,
		MessageTypeAlt: raw.MessageTypeAlt,
		Content:        rawMessageString(raw.Content),
		CreateTime:     raw.CreateTime,
		Sender:         raw.Sender,
		Mentions:       raw.Mentions,
	}
	return nil
}

func (m MessageItem) EventMessage() EventMessage {
	return EventMessage{
		MessageID:   m.MessageID,
		RootID:      m.RootID,
		ParentID:    m.ParentID,
		ThreadID:    m.ThreadID,
		ChatID:      m.ChatID,
		ChatType:    m.ChatType,
		MessageType: firstNonEmptyString(m.MessageType, m.MessageTypeAlt),
		Content:     m.Content,
		CreateTime:  m.CreateTime,
		Mentions:    m.Mentions,
	}
}

func rawMessageString(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	return string(raw)
}

type UserInfoResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		User struct {
			OpenID      string `json:"open_id"`
			Name        string `json:"name"`
			DisplayName string `json:"display_name"`
			Nickname    string `json:"nickname"`
			EnName      string `json:"en_name"`
		} `json:"user"`
	} `json:"data"`
}

func (r UserInfoResponse) DisplayName() string {
	return firstNonEmptyString(r.Data.User.Name, r.Data.User.DisplayName, r.Data.User.Nickname, r.Data.User.EnName)
}

type ChatInfoResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		ChatID    string `json:"chat_id"`
		Name      string `json:"name"`
		Avatar    string `json:"avatar"`
		AvatarURL string `json:"avatar_url"`
		Chat      struct {
			ChatID    string `json:"chat_id"`
			Name      string `json:"name"`
			Avatar    string `json:"avatar"`
			AvatarURL string `json:"avatar_url"`
		} `json:"chat"`
	} `json:"data"`
}

func (r ChatInfoResponse) DisplayName() string {
	return firstNonEmptyString(r.Data.Name, r.Data.Chat.Name)
}

func (r ChatInfoResponse) AvatarURL() string {
	return firstNonEmptyString(r.Data.AvatarURL, r.Data.Avatar, r.Data.Chat.AvatarURL, r.Data.Chat.Avatar)
}

type ChatMembersResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		Items []ChatMember `json:"items"`
	} `json:"data"`
}

type ChatMember struct {
	MemberID     string `json:"member_id"`
	MemberIDType string `json:"member_id_type"`
	Name         string `json:"name"`
	TenantKey    string `json:"tenant_key"`
}

func (r ChatMembersResponse) DisplayNameForOpenID(openID string) string {
	openID = strings.TrimSpace(openID)
	if openID == "" {
		return ""
	}
	for _, item := range r.Data.Items {
		if strings.TrimSpace(item.MemberID) == openID {
			return strings.TrimSpace(item.Name)
		}
	}
	return ""
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

func (id *SenderID) UnmarshalJSON(data []byte) error {
	raw := strings.TrimSpace(string(data))
	if raw == "" || raw == "null" {
		return nil
	}
	if strings.HasPrefix(raw, "{") {
		var object struct {
			OpenID  string `json:"open_id"`
			UserID  string `json:"user_id"`
			UnionID string `json:"union_id"`
		}
		if err := json.Unmarshal(data, &object); err != nil {
			return err
		}
		id.OpenID = strings.TrimSpace(object.OpenID)
		id.UserID = strings.TrimSpace(object.UserID)
		id.UnionID = strings.TrimSpace(object.UnionID)
		return nil
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	value = strings.TrimSpace(value)
	switch {
	case strings.HasPrefix(value, "ou_"):
		id.OpenID = value
	case strings.HasPrefix(value, "on_"):
		id.UnionID = value
	default:
		id.UserID = value
	}
	return nil
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
	DisplayName   string
	AvatarURL     string
}

func ParseWebhook(data []byte) (*Webhook, error) {
	var hook Webhook
	if err := json.Unmarshal(data, &hook); err != nil {
		return nil, fmt.Errorf("decode lark webhook: %w", err)
	}
	return &hook, nil
}

func ParseEvent(data []byte) (*Webhook, error) {
	hook, err := ParseWebhook(data)
	if err != nil {
		return nil, err
	}
	if hook.IsURLVerification() ||
		hook.Header.EventType != "" ||
		hook.Event.Message.MessageID != "" ||
		hook.Event.Message.ChatID != "" ||
		hook.Event.Sender.SenderID.OpenID != "" ||
		hook.Event.Sender.SenderID.UserID != "" ||
		hook.Event.Sender.SenderID.UnionID != "" {
		return hook, nil
	}

	var flat struct {
		AppID      string       `json:"app_id"`
		EventID    string       `json:"event_id"`
		EventType  string       `json:"event_type"`
		Token      string       `json:"token"`
		CreateTime string       `json:"create_time"`
		TenantKey  string       `json:"tenant_key"`
		Sender     EventSender  `json:"sender"`
		Message    EventMessage `json:"message"`
	}
	if err := json.Unmarshal(data, &flat); err != nil {
		return nil, fmt.Errorf("decode lark event: %w", err)
	}
	if flat.Message.MessageID == "" && flat.Message.ChatID == "" &&
		flat.Sender.SenderID.OpenID == "" && flat.Sender.SenderID.UserID == "" && flat.Sender.SenderID.UnionID == "" {
		return hook, nil
	}
	eventType := strings.TrimSpace(flat.EventType)
	if eventType == "" {
		eventType = strings.TrimSpace(hook.Type)
	}
	if eventType == "" {
		eventType = EventTypeMessageReceive
	}
	return &Webhook{
		Type:  eventType,
		Token: flat.Token,
		Header: EventHeader{
			EventID:    flat.EventID,
			EventType:  eventType,
			AppID:      flat.AppID,
			Token:      flat.Token,
			CreateTime: flat.CreateTime,
			TenantKey:  flat.TenantKey,
		},
		Event: MessageEvent{
			Sender:  flat.Sender,
			Message: flat.Message,
		},
	}, nil
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
	return m.TextWithMentionFilter(nil)
}

func (m EventMessage) TextWithMentionFilter(strip func(Mention) bool) string {
	messageType := strings.TrimSpace(m.MessageType)
	if messageType == "" || messageType == "text" {
		var parsed struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(m.Content), &parsed); err == nil && parsed.Text != "" {
			return normalizeText(resolveMentionPlaceholders(parsed.Text, m.Mentions, strip))
		}
		return normalizeText(resolveMentionPlaceholders(m.Content, m.Mentions, strip))
	}
	if messageType == "post" {
		return normalizeText(resolveMentionPlaceholders(postContentText(m.Content, m.Mentions, strip), m.Mentions, strip))
	}
	if isCardMessageType(messageType) {
		return normalizeText(resolveMentionPlaceholders(cardContentText(m.Content), m.Mentions, strip))
	}
	return ""
}

func isCardMessageType(messageType string) bool {
	switch strings.ToLower(strings.TrimSpace(messageType)) {
	case "interactive", "card", "raw_card_content", "template_card":
		return true
	default:
		return false
	}
}

func postContentText(raw string, mentions []Mention, strip func(Mention) bool) string {
	var parsed any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return strings.TrimSpace(raw)
	}
	body := unwrapPostLocale(parsed)
	if body == nil {
		return strings.TrimSpace(renderPostContentValue(parsed, mentions, strip))
	}
	return renderPostBody(body, mentions, strip)
}

func renderPostBody(body map[string]any, mentions []Mention, strip func(Mention) bool) string {
	var lines []string
	if title := strings.TrimSpace(anyString(body["title"])); title != "" {
		lines = append(lines, "**"+title+"**", "")
	}
	for _, paragraph := range anySlice(body["content"]) {
		var line strings.Builder
		if items := anySlice(paragraph); len(items) > 0 {
			for _, item := range items {
				line.WriteString(renderPostContentValue(item, mentions, strip))
			}
		} else {
			line.WriteString(renderPostContentValue(paragraph, mentions, strip))
		}
		lines = append(lines, strings.TrimSpace(line.String()))
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func cardContentText(raw string) string {
	var parsed any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return strings.TrimSpace(raw)
	}
	var parts []string
	appendCardText(&parts, parsed, 0)
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func appendCardText(parts *[]string, value any, depth int) {
	if depth > 8 {
		return
	}
	switch item := value.(type) {
	case nil:
		return
	case string:
		text := strings.TrimSpace(item)
		if text == "" {
			return
		}
		var nested any
		if err := json.Unmarshal([]byte(text), &nested); err == nil {
			before := len(*parts)
			appendCardText(parts, nested, depth+1)
			if len(*parts) > before {
				return
			}
		}
		appendTextPart(parts, text)
	case []any:
		for _, child := range item {
			appendCardText(parts, child, depth+1)
		}
	case map[string]any:
		appendCardMapText(parts, item, depth+1)
	}
}

func appendCardMapText(parts *[]string, item map[string]any, depth int) {
	if tag := strings.ToLower(strings.TrimSpace(anyString(item["tag"]))); tag != "" {
		switch tag {
		case "plain_text", "lark_md", "markdown", "md":
			appendCardText(parts, firstNonEmptyAny(item["content"], item["text"]), depth)
		case "div":
			appendCardText(parts, item["text"], depth)
			appendCardText(parts, item["fields"], depth)
			appendCardText(parts, item["extra"], depth)
		case "note":
			appendCardText(parts, item["elements"], depth)
		case "action":
			appendCardText(parts, item["actions"], depth)
		case "button":
			appendCardText(parts, item["text"], depth)
		case "img":
			appendCardText(parts, item["alt"], depth)
		case "column_set":
			appendCardText(parts, item["columns"], depth)
		case "column":
			appendCardText(parts, item["elements"], depth)
		case "hr":
			return
		default:
			appendCardKnownFields(parts, item, depth)
		}
		return
	}
	appendCardKnownFields(parts, item, depth)
}

func appendCardKnownFields(parts *[]string, item map[string]any, depth int) {
	for _, key := range []string{
		"card", "card_content", "raw_card_content",
		"header", "title", "subtitle",
		"body", "elements", "content",
		"text", "fields", "actions", "extra",
		"alt", "placeholder", "options",
		"fallback", "summary", "description",
	} {
		if value, ok := item[key]; ok {
			appendCardText(parts, value, depth)
		}
	}
}

func appendTextPart(parts *[]string, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	*parts = append(*parts, text)
}

func renderPostContentValue(value any, mentions []Mention, strip func(Mention) bool) string {
	switch item := value.(type) {
	case nil:
		return ""
	case string:
		text := strings.TrimSpace(item)
		if text == "" {
			return ""
		}
		var nested any
		if err := json.Unmarshal([]byte(text), &nested); err == nil {
			if rendered := strings.TrimSpace(renderPostContentValue(nested, mentions, strip)); rendered != "" {
				return rendered
			}
		}
		return text
	case []any:
		var lines []string
		for _, child := range item {
			if text := strings.TrimSpace(renderPostContentValue(child, mentions, strip)); text != "" {
				lines = append(lines, text)
			}
		}
		return strings.Join(lines, "\n")
	case map[string]any:
		if tag := strings.TrimSpace(anyString(item["tag"])); tag != "" {
			return renderPostElement(item, mentions, strip)
		}
		if body := unwrapPostLocale(item); body != nil {
			return renderPostBody(body, mentions, strip)
		}
		for _, key := range []string{"content", "text", "plain_text", "fallback", "summary"} {
			if text := strings.TrimSpace(renderPostContentValue(item[key], mentions, strip)); text != "" {
				return text
			}
		}
		return ""
	default:
		return ""
	}
}

func unwrapPostLocale(parsed any) map[string]any {
	return unwrapPostLocaleDepth(parsed, 0)
}

func unwrapPostLocaleDepth(value any, depth int) map[string]any {
	parsed, ok := value.(map[string]any)
	if !ok {
		if text, ok := value.(string); ok {
			text = strings.TrimSpace(text)
			if text == "" {
				return nil
			}
			var nested any
			if err := json.Unmarshal([]byte(text), &nested); err == nil {
				return unwrapPostLocaleDepth(nested, depth+1)
			}
		}
		return nil
	}
	if parsed == nil || depth > 4 {
		return nil
	}
	if isPostBody(parsed) {
		return parsed
	}
	if post, ok := parsed["post"].(map[string]any); ok {
		if body := unwrapPostLocaleDepth(post, depth+1); body != nil {
			return body
		}
	}
	if content, ok := parsed["content"].(map[string]any); ok {
		if body := unwrapPostLocaleDepth(content, depth+1); body != nil {
			return body
		}
	}
	if content, ok := parsed["content"].(string); ok {
		if body := unwrapPostLocaleDepth(content, depth+1); body != nil {
			return body
		}
	}
	for _, locale := range []string{"zh_cn", "en_us", "ja_jp"} {
		if body, ok := parsed[locale].(map[string]any); ok {
			return body
		}
	}
	for _, value := range parsed {
		if body, ok := value.(map[string]any); ok {
			if unwrapped := unwrapPostLocaleDepth(body, depth+1); unwrapped != nil {
				return unwrapped
			}
		}
	}
	return nil
}

func isPostBody(parsed map[string]any) bool {
	if parsed == nil {
		return false
	}
	if _, ok := parsed["content"].([]any); ok {
		return true
	}
	if _, ok := parsed["title"]; !ok {
		return false
	}
	_, contentIsMap := parsed["content"].(map[string]any)
	return !contentIsMap
}

func renderPostElement(value any, mentions []Mention, strip func(Mention) bool) string {
	el, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	switch anyString(el["tag"]) {
	case "text":
		return applyPostStyle(anyString(el["text"]), anyStringSlice(el["style"]))
	case "md", "markdown", "lark_md":
		return anyString(firstNonEmptyAny(el["text"], el["content"]))
	case "a":
		text := anyString(el["text"])
		href := anyString(el["href"])
		if text == "" {
			text = href
		}
		if href == "" {
			return text
		}
		return "[" + text + "](" + href + ")"
	case "at":
		userID := anyString(el["user_id"])
		if strings.EqualFold(userID, "all") {
			return "@all"
		}
		name := anyString(el["user_name"])
		if mention, ok := mentionByID(mentions, userID); ok {
			if strip != nil && strip(mention) {
				return ""
			}
			if mention.Key != "" {
				return mention.Key
			}
			if strings.TrimSpace(mention.Name) != "" {
				name = mention.Name
			}
		}
		if name == "" {
			name = userID
		}
		if name == "" {
			return ""
		}
		return "@" + strings.TrimPrefix(name, "@")
	case "img":
		if key := anyString(el["image_key"]); key != "" {
			return "![image](" + key + ")"
		}
		return ""
	case "media":
		if key := anyString(el["file_key"]); key != "" {
			return `<file key="` + key + `"/>`
		}
		return ""
	case "code_block":
		lang := anyString(el["language"])
		code := anyString(el["text"])
		return "\n```" + lang + "\n" + code + "\n```\n"
	case "hr":
		return "\n---\n"
	default:
		return anyString(firstNonEmptyAny(el["text"], el["content"]))
	}
}

func applyPostStyle(text string, style []string) string {
	for _, item := range style {
		switch item {
		case "bold":
			text = "**" + text + "**"
		case "italic":
			text = "*" + text + "*"
		case "underline":
			text = "<u>" + text + "</u>"
		case "lineThrough":
			text = "~~" + text + "~~"
		case "codeInline":
			text = "`" + text + "`"
		}
	}
	return text
}

func mentionByID(mentions []Mention, id string) (Mention, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Mention{}, false
	}
	for _, mention := range mentions {
		if strings.TrimSpace(mention.ID.OpenID) == id ||
			strings.TrimSpace(mention.ID.UserID) == id ||
			strings.TrimSpace(mention.ID.UnionID) == id {
			return mention, true
		}
	}
	return Mention{}, false
}

func anyString(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return text
}

func anySlice(value any) []any {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	return items
}

func anyStringSlice(value any) []string {
	items := anySlice(value)
	out := make([]string, 0, len(items))
	for _, item := range items {
		if text := strings.TrimSpace(anyString(item)); text != "" {
			out = append(out, text)
		}
	}
	return out
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func firstNonEmptyAny(values ...any) any {
	for _, value := range values {
		if strings.TrimSpace(anyString(value)) != "" {
			return value
		}
	}
	return nil
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

var (
	atTagRE    = regexp.MustCompile(`(?i)<at\s+[^>]*>(.*?)</at>`)
	spaceRunRE = regexp.MustCompile(`[ \t]{2,}`)
)

func resolveMentionPlaceholders(text string, mentions []Mention, strip func(Mention) bool) string {
	for _, mention := range mentions {
		key := strings.TrimSpace(mention.Key)
		if key == "" {
			continue
		}
		replacement := ""
		if strip != nil && strip(mention) {
			text = strings.ReplaceAll(text, key, replacement)
			continue
		}
		if key == "@_all" {
			replacement = "@all"
		} else if name := strings.TrimSpace(mention.Name); name != "" {
			replacement = "@" + strings.TrimPrefix(name, "@")
		}
		text = strings.ReplaceAll(text, key, replacement)
	}
	return text
}

func normalizeText(text string) string {
	text = atTagRE.ReplaceAllString(text, "$1")
	text = spaceRunRE.ReplaceAllString(text, " ")
	return strings.TrimSpace(text)
}
