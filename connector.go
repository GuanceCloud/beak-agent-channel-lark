package beaklark

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/GuanceCloud/beak-agent-channel-lark/internal/lark"
	"github.com/GuanceCloud/beak-agent-channel-lark/sdk"
	"github.com/GuanceCloud/beak-agent-channel-lark/state"
)

var ErrCredentialLogin = errors.New("lark connector uses credential login; create channel account from CredentialSchema")

const (
	credentialValidationAttempts   = 2
	credentialValidationRetryDelay = 100 * time.Millisecond
)

type Connector struct {
	channel Channel
}

type WebhookResult struct {
	Type        string              `json:"type"`
	Challenge   string              `json:"challenge,omitempty"`
	Ignored     bool                `json:"ignored,omitempty"`
	Reason      string              `json:"reason,omitempty"`
	SessionUUID string              `json:"session_uuid,omitempty"`
	MessageUUID string              `json:"message_uuid,omitempty"`
	Inbound     *sdk.InboundMessage `json:"inbound,omitempty"`
}

type EventResult = WebhookResult

type WebhookConnector interface {
	sdk.Connector
	HandleWebhook(ctx context.Context, runtime sdk.Runtime, account sdk.ChannelAccount, body []byte) (*WebhookResult, error)
}

type EventConnector interface {
	sdk.Connector
	HandleEvent(ctx context.Context, runtime sdk.Runtime, account sdk.ChannelAccount, body []byte) (*EventResult, error)
}

type WebhookRequestConnector interface {
	sdk.Connector
	HandleWebhookRequest(ctx context.Context, runtime sdk.Runtime, account sdk.ChannelAccount, req *http.Request) (*sdk.WebhookResponse, error)
}

func NewConnector() sdk.Connector {
	return Connector{channel: Channel{}}
}

func (c Connector) Metadata() sdk.ConnectorMetadata {
	meta := c.channel.Metadata()
	caps := c.channel.Capabilities()
	return sdk.ConnectorMetadata{
		ID:          meta.ID,
		Platform:    meta.Platform,
		Label:       meta.Label,
		Description: meta.Description,
		Capabilities: sdk.Capabilities{
			LoginModes:       []string{sdk.LoginModeCredential},
			Text:             caps.Text,
			Media:            caps.Media,
			GroupChat:        caps.GroupChat,
			DirectChat:       caps.DirectChat,
			Stream:           true,
			Webhook:          false,
			BlockStreaming:   caps.BlockStreaming,
			AckModes:         []string{"reaction"},
			RuntimeOwnership: sdk.RuntimeOwnershipHostStream,
		},
	}
}

func (c Connector) CredentialSchema(context.Context) sdk.CredentialSchema {
	schema := c.channel.SettingsSchema()
	properties := make(map[string]sdk.CredentialField, len(schema.Properties))
	for key, raw := range schema.Properties {
		item, _ := raw.(map[string]any)
		properties[key] = sdk.CredentialField{
			Type:        stringValue(item["type"]),
			Title:       stringValue(item["title"]),
			Description: stringValue(item["description"]),
			Secret:      boolValue(item["secret"]),
		}
	}
	return sdk.CredentialSchema{
		Type:                 schema.Type,
		LoginModes:           []string{sdk.LoginModeCredential},
		Properties:           properties,
		Required:             schema.Required,
		AdditionalProperties: false,
	}
}

func (Connector) ValidateCredential(ctx context.Context, req sdk.CredentialValidationRequest) (*sdk.CredentialValidationResult, error) {
	credential := cloneMap(req.Credential)
	state := cloneMap(req.State)
	platform := credentialValidationPlatform(req, credential)
	client := lark.NewClient(baseURLFromCredential(credential), stringValue(credential["app_id"]), stringValue(credential["app_secret"]))
	client.HTTPClient = req.Runtime.HTTPClient

	now := time.Now().UTC()
	token, expiresAt, info, err := validateLarkCredential(ctx, client, now)
	if err != nil {
		if lark.IsCredentialRejected(err) {
			return credentialValidationFailure(credential, state, platform, err), nil
		}
		return nil, fmt.Errorf("%s credential validation failed: %w", platform, err)
	}

	accountKey := firstString(credential["account_id"], credential["app_id"])
	if accountKey != "" {
		credential["account_id"] = accountKey
	}
	displayName := firstString(credential["display_name"], info.Bot.AppName, credential["app_id"])
	if strings.TrimSpace(info.Bot.OpenID) != "" {
		credential["bot_open_id"] = strings.TrimSpace(info.Bot.OpenID)
		state["bot_open_id"] = strings.TrimSpace(info.Bot.OpenID)
	}
	if strings.TrimSpace(info.Bot.AppName) != "" {
		credential["bot_name"] = strings.TrimSpace(info.Bot.AppName)
		state["bot_name"] = strings.TrimSpace(info.Bot.AppName)
	}
	if identities := larkBotIdentityState(larkBotIdentity{
		OpenID: firstString(credential["bot_open_id"], state["bot_open_id"]),
		Name:   firstString(credential["bot_name"], credential["bot_app_name"], state["bot_name"], state["bot_app_name"]),
	}); len(identities) > 0 {
		state["bot_identities"] = identities
		state["bot_identity"] = identities[0]
	}
	state["tenant_access_token"] = token
	state["tenant_access_token_expires_at"] = expiresAt

	return &sdk.CredentialValidationResult{
		Valid:       true,
		AccountKey:  accountKey,
		DisplayName: displayName,
		Credential:  credential,
		State:       state,
		Metadata: map[string]any{
			"platform":        platform,
			"activate_status": info.Bot.ActivateStatus,
			"avatar_url":      info.Bot.AvatarURL,
			"ip_white_list":   info.Bot.IPWhiteList,
		},
	}, nil
}

func validateLarkCredential(ctx context.Context, client *lark.Client, now time.Time) (string, time.Time, *lark.BotInfoResponse, error) {
	var lastErr error
	for attempt := 0; attempt < credentialValidationAttempts; attempt++ {
		token, expiresAt, err := client.TenantAccessTokenWithExpiry(ctx, now)
		if err == nil {
			info, infoErr := client.BotInfo(ctx)
			if infoErr == nil {
				return token, expiresAt, info, nil
			}
			err = infoErr
		}
		lastErr = err
		if lark.IsCredentialRejected(err) || !lark.IsRetryableError(err) || attempt+1 == credentialValidationAttempts {
			break
		}
		if err := waitForCredentialRetry(ctx); err != nil {
			return "", time.Time{}, nil, err
		}
	}
	return "", time.Time{}, nil, lastErr
}

func waitForCredentialRetry(ctx context.Context) error {
	timer := time.NewTimer(credentialValidationRetryDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (Connector) StartLogin(context.Context, sdk.LoginStartRequest) (*sdk.LoginChallenge, error) {
	return nil, ErrCredentialLogin
}

func (Connector) PollLogin(context.Context, sdk.LoginPollRequest) (*sdk.LoginStatus, error) {
	return nil, ErrCredentialLogin
}

func (c Connector) Start(ctx context.Context, runtime sdk.Runtime) error {
	if runtime.Gateway == nil {
		return fmt.Errorf("lark connector requires sdk.Runtime.Gateway")
	}
	channelPlatform := effectiveChannelPlatform(runtime)
	if _, err := runtime.Gateway.EnsureChannel(ctx, sdk.EnsureChannelRequest{
		WorkspaceUUID: runtime.WorkspaceUUID,
		Platform:      channelPlatform,
		Name:          "Lark/Feishu",
		Config:        map[string]any{"bridge": ID},
	}); err != nil {
		return err
	}
	store := newConnectorStateStore(runtime.AccountStore)
	for _, account := range runtimeAccountCandidates(runtime) {
		store.seed(account)
		accountUUID := accountKey(account)
		if accountUUID == "" {
			return fmt.Errorf("lark account_uuid or app_id is required")
		}
		accountPlatform := effectiveAccountPlatform(runtime, account)
		sessionUUID, err := runtime.Gateway.EnsureChannelLinkSession(ctx, sdk.EnsureChannelLinkSessionRequest{
			WorkspaceUUID:       runtime.WorkspaceUUID,
			Platform:            accountPlatform,
			AccountUUID:         accountUUID,
			AgentParticipantID:  runtime.Gateway.AgentParticipantID(),
			BridgeParticipantID: runtime.Gateway.BridgeParticipantID(accountPlatform),
		})
		if err != nil {
			return err
		}
		state, err := store.LoadAccount(ctx, accountUUID)
		if err != nil {
			return err
		}
		if _, err := ensureLarkBotIdentity(ctx, runtime, account, state, store, false); err != nil {
			return err
		}
		if state.ChannelLinkSession != sessionUUID {
			state.ChannelLinkSession = sessionUUID
			if err := store.SaveAccount(ctx, state); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c Connector) Send(ctx context.Context, runtime sdk.Runtime, req sdk.OutboundMessage) (*sdk.SendResult, error) {
	account, err := selectRuntimeAccount(runtime, req.AccountUUID)
	if err != nil {
		return nil, err
	}
	accountUUID := accountKey(account)
	platform := effectiveAccountPlatform(runtime, account)
	store := newConnectorStateStore(runtime.AccountStore)
	store.seed(account)
	accountState, err := store.LoadAccount(ctx, accountUUID)
	if err != nil {
		return nil, err
	}
	client := clientFromAccount(runtime, account)
	now := time.Now().UTC()
	if accountState.TenantAccessToken != "" && accountState.TokenExpiresAt.After(now.Add(5*time.Minute)) {
		client.TenantToken = accountState.TenantAccessToken
		client.TenantTokenExpiresAt = accountState.TokenExpiresAt
	}
	if client.TenantToken == "" || !client.TenantTokenExpiresAt.After(now.Add(5*time.Minute)) {
		token, expiresAt, err := client.TenantAccessTokenWithExpiry(ctx, now)
		if err != nil {
			return nil, err
		}
		accountState.TenantAccessToken = token
		accountState.TokenExpiresAt = expiresAt
		if err := store.SaveAccount(ctx, accountState); err != nil {
			return nil, err
		}
	}
	target := strings.TrimSpace(req.ChatID)
	if target == "" {
		return nil, fmt.Errorf("lark outbound chat_id is required")
	}
	msgType := outboundMsgType(req)
	content, err := outboundContent(req, msgType)
	if err != nil {
		return nil, err
	}
	var resp *lark.SendMessageResponse
	replyTo := firstString(req.Raw["reply_to_message_id"], req.Raw["parent_message_id"])
	replyInThread := optionalBool(req.Raw["reply_in_thread"])
	if replyTo == "" {
		replyTo, replyInThread, err = larkReplyTarget(ctx, client, req.ThreadID)
		if err != nil {
			return nil, err
		}
	} else if replyInThread == nil && strings.HasPrefix(strings.TrimSpace(req.ThreadID), "omt_") {
		value := true
		replyInThread = &value
	}
	if replyTo != "" {
		resp, err = client.ReplyMessage(ctx, lark.ReplyMessageRequest{
			MessageID:     replyTo,
			MsgType:       msgType,
			Content:       content,
			UUID:          req.MessageUUID,
			ReplyInThread: replyInThread,
		})
	} else {
		resp, err = client.SendMessage(ctx, lark.SendMessageRequest{
			ReceiveID:     target,
			ReceiveIDType: receiveIDType(req),
			MsgType:       msgType,
			Content:       content,
			UUID:          req.MessageUUID,
		})
	}
	if err != nil {
		return nil, err
	}
	result := &sdk.SendResult{
		Platform:    platform,
		AccountUUID: accountUUID,
		MessageID:   resp.Data.MessageID,
		Raw: map[string]any{
			"chat_id":  resp.Data.ChatID,
			"msg_type": msgType,
		},
	}
	if threadID := strings.TrimSpace(req.ThreadID); threadID != "" {
		result.Raw["thread_id"] = threadID
		result.Raw["reply_to_message_id"] = replyTo
	}
	return result, nil
}

func larkReplyTarget(ctx context.Context, client *lark.Client, threadID string) (string, *bool, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return "", nil, nil
	}
	if strings.HasPrefix(threadID, "om_") {
		return threadID, nil, nil
	}
	message, err := client.LatestThreadMessage(ctx, threadID)
	if err != nil {
		return "", nil, fmt.Errorf("resolve lark thread %s reply target: %w", threadID, err)
	}
	value := true
	return strings.TrimSpace(message.MessageID), &value, nil
}

func (c Connector) Acknowledge(ctx context.Context, runtime sdk.Runtime, req sdk.OutboundAck) (*sdk.AckResult, error) {
	account, err := selectRuntimeAccount(runtime, req.AccountUUID)
	if err != nil {
		return nil, err
	}
	accountUUID := accountKey(account)
	platform := effectiveAccountPlatform(runtime, account)
	result := &sdk.AckResult{
		Platform:    platform,
		AccountUUID: accountUUID,
		Mode:        "reaction",
		Status:      "skipped",
	}
	if mode := larkUnsupportedAckMode(req); mode != "" {
		result.Mode = mode
		result.Status = "unsupported"
		result.Raw = map[string]any{"reason": "unsupported_ack_mode"}
		return result, nil
	}
	if !larkAckWantsReaction(req) {
		return result, nil
	}
	messageID := larkAckTargetMessageID(req)
	if messageID == "" {
		result.Raw = map[string]any{"reason": "missing_target_message_id"}
		return result, nil
	}

	store := newConnectorStateStore(runtime.AccountStore)
	store.seed(account)
	accountState, err := store.LoadAccount(ctx, accountUUID)
	if err != nil {
		return nil, err
	}
	client := clientFromAccount(runtime, account)
	now := time.Now().UTC()
	if accountState.TenantAccessToken != "" && accountState.TokenExpiresAt.After(now.Add(5*time.Minute)) {
		client.TenantToken = accountState.TenantAccessToken
		client.TenantTokenExpiresAt = accountState.TokenExpiresAt
	}
	if client.TenantToken == "" || !client.TenantTokenExpiresAt.After(now.Add(5*time.Minute)) {
		token, expiresAt, err := client.TenantAccessTokenWithExpiry(ctx, now)
		if err != nil {
			return nil, err
		}
		accountState.TenantAccessToken = token
		accountState.TokenExpiresAt = expiresAt
		if err := store.SaveAccount(ctx, accountState); err != nil {
			return nil, err
		}
	}

	emojiType := larkAckEmojiType(req)
	resp, err := client.AddMessageReaction(ctx, lark.AddMessageReactionRequest{
		MessageID: messageID,
		EmojiType: emojiType,
	})
	if err != nil {
		return nil, err
	}
	result.Status = "sent"
	result.ReactionID = resp.Data.ReactionID
	result.Raw = map[string]any{
		"message_id":  messageID,
		"emoji_type":  resp.Data.ReactionType.EmojiType,
		"action_time": resp.Data.ActionTime,
	}
	return result, nil
}

func (Connector) Stop(context.Context, sdk.ChannelAccount) error {
	return nil
}

func (c Connector) HandleWebhook(ctx context.Context, runtime sdk.Runtime, account sdk.ChannelAccount, body []byte) (*WebhookResult, error) {
	decoded, err := lark.DecodeWebhookBody(body, stringValue(account.Credential["encrypt_key"]))
	if err != nil {
		return nil, err
	}
	hook, err := lark.ParseEvent(decoded)
	if err != nil {
		return nil, err
	}
	if hook.IsURLVerification() {
		if !hook.VerifyToken(stringValue(account.Credential["verification_token"])) {
			return nil, fmt.Errorf("lark webhook verification token mismatch")
		}
		return &WebhookResult{Type: "url_verification", Challenge: hook.Challenge}, nil
	}
	if hook.EventType() != lark.EventTypeMessageReceive {
		return &WebhookResult{Type: hook.EventType(), Ignored: true, Reason: "unsupported_event_type"}, nil
	}
	return c.processMessageEvent(ctx, runtime, account, hook, true)
}

func (c Connector) HandleEvent(ctx context.Context, runtime sdk.Runtime, account sdk.ChannelAccount, body []byte) (*EventResult, error) {
	hook, err := lark.ParseEvent(body)
	if err != nil {
		return nil, err
	}
	if hook.EventType() != lark.EventTypeMessageReceive {
		return &WebhookResult{Type: hook.EventType(), Ignored: true, Reason: "unsupported_event_type"}, nil
	}
	return c.processMessageEvent(ctx, runtime, account, hook, false)
}

func (c Connector) HandleWebhookRequest(ctx context.Context, runtime sdk.Runtime, account sdk.ChannelAccount, req *http.Request) (*sdk.WebhookResponse, error) {
	if req == nil || req.Body == nil {
		return nil, fmt.Errorf("lark webhook request body is required")
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	signature := firstString(req.Header.Get("X-Lark-Signature"), req.Header.Get("X-Lark-Request-Signature"))
	timestamp := firstString(req.Header.Get("X-Lark-Request-Timestamp"), req.Header.Get("X-Lark-Timestamp"))
	nonce := firstString(req.Header.Get("X-Lark-Request-Nonce"), req.Header.Get("X-Lark-Nonce"))
	encryptKey := stringValue(account.Credential["encrypt_key"])
	if err := verifyLarkWebhookRequestSignature(timestamp, nonce, encryptKey, body, signature, time.Now().UTC()); err != nil {
		return nil, err
	}
	result, err := c.HandleWebhook(ctx, runtime, account, body)
	if err != nil {
		return nil, err
	}
	return larkWebhookResponse(result)
}

func verifyLarkWebhookRequestSignature(timestamp, nonce, encryptKey string, body []byte, signature string, now time.Time) error {
	if strings.TrimSpace(signature) == "" || strings.TrimSpace(timestamp) == "" || strings.TrimSpace(nonce) == "" {
		return fmt.Errorf("lark webhook signature headers are required")
	}
	if strings.TrimSpace(encryptKey) == "" {
		return fmt.Errorf("lark webhook signature verification requires encrypt_key")
	}
	seconds, err := strconv.ParseInt(strings.TrimSpace(timestamp), 10, 64)
	if err != nil {
		return fmt.Errorf("lark webhook timestamp is invalid")
	}
	sentAt := time.Unix(seconds, 0).UTC()
	age := now.Sub(sentAt)
	if age < 0 {
		age = -age
	}
	if age > time.Hour {
		return fmt.Errorf("lark webhook timestamp is expired")
	}
	if !lark.VerifyWebhookSignature(timestamp, nonce, encryptKey, body, signature) {
		return fmt.Errorf("lark webhook signature mismatch")
	}
	return nil
}

func larkWebhookResponse(result *WebhookResult) (*sdk.WebhookResponse, error) {
	if result != nil && strings.TrimSpace(result.Type) == "url_verification" && strings.TrimSpace(result.Challenge) != "" {
		return jsonWebhookResponse(map[string]string{"challenge": result.Challenge})
	}
	return jsonWebhookResponse(map[string]string{"msg": "success"})
}

func jsonWebhookResponse(value any) (*sdk.WebhookResponse, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return &sdk.WebhookResponse{
		StatusCode: http.StatusOK,
		Headers:    map[string]string{"content-type": "application/json; charset=utf-8"},
		Body:       body,
	}, nil
}

func (c Connector) processMessageEvent(ctx context.Context, runtime sdk.Runtime, account sdk.ChannelAccount, hook *lark.Webhook, verifyToken bool) (*WebhookResult, error) {
	if runtime.Gateway == nil {
		return nil, fmt.Errorf("lark event handling requires sdk.Runtime.Gateway")
	}
	if verifyToken && !hook.VerifyToken(stringValue(account.Credential["verification_token"])) {
		return nil, fmt.Errorf("lark webhook verification token mismatch")
	}
	if !larkEventOwnershipValid(account, hook) {
		return &WebhookResult{Type: hook.EventType(), Ignored: true, Reason: "app_id_mismatch"}, nil
	}
	accountUUID := accountKey(account)
	if accountUUID == "" {
		return nil, fmt.Errorf("lark account_uuid or app_id is required")
	}
	platform := effectiveAccountPlatform(runtime, account)
	store := newConnectorStateStore(runtime.AccountStore)
	store.seed(account)
	state, err := store.LoadAccount(ctx, accountUUID)
	if err != nil {
		return nil, err
	}
	bot, err := ensureLarkBotIdentity(ctx, runtime, account, state, store, larkMessageNeedsBotName(hook.Event.Message))
	if err != nil {
		return nil, err
	}
	senderID := larkSenderID(hook.Event.Sender.SenderID)
	if larkSenderMatchesBot(hook.Event.Sender.SenderID, bot) {
		return &WebhookResult{Type: hook.EventType(), Ignored: true, Reason: "self_echo"}, nil
	}
	text := hook.Event.Message.TextWithMentionFilter(func(mention lark.Mention) bool {
		return larkMentionMatchesBot(mention, bot)
	})
	chat := hook.Event.Message.ChatIdentity(senderID)
	inbound := buildInboundMessageForPlatform(platform, runtime.WorkspaceUUID, runtime.Channel.UUID, accountUUID, hook, text, bot)
	if chat.ChatType == "" || chat.ChatID == "" || chat.SenderID == "" {
		return &WebhookResult{Type: hook.EventType(), Ignored: true, Reason: "incomplete_or_non_text_message"}, nil
	}
	if strings.TrimSpace(text) == "" && !inbound.MentionedMe {
		return &WebhookResult{Type: hook.EventType(), Ignored: true, Reason: "incomplete_or_non_text_message"}, nil
	}
	threadID := larkThreadID(hook.Event.Message)
	stateKey := chat.StateKey()
	key := hook.DedupeKey(accountUUID)
	if _, ok := state.InboundSeen[key]; ok {
		return &WebhookResult{Type: hook.EventType(), Ignored: true, Reason: "duplicate", SessionUUID: state.PeerSessions[stateKey]}, nil
	}
	senderDisplayName := resolveLarkDisplayNames(ctx, runtime, account, state, &chat, hook.Event.Sender)
	inbound = buildInboundMessageWithIdentityForPlatform(platform, runtime.WorkspaceUUID, runtime.Channel.UUID, accountUUID, hook, text, bot, chat, senderDisplayName)
	inbound.ReferencedMessage = resolveLarkReferencedMessage(ctx, runtime, account, state, platform, hook.Event.Message)

	sessionUUID, err := runtime.Gateway.EnsureChatSession(ctx, sdk.EnsureChatSessionRequest{
		WorkspaceUUID:       runtime.WorkspaceUUID,
		Platform:            platform,
		AccountUUID:         accountUUID,
		ChatType:            chat.ChatType,
		ChatID:              chat.ChatID,
		ThreadID:            threadID,
		ChatDisplayName:     chat.DisplayName,
		ChatAvatarURL:       chat.AvatarURL,
		ChatIdentity:        larkSDKChatIdentity(chat),
		SenderID:            chat.SenderID,
		AgentParticipantID:  runtime.Gateway.AgentParticipantID(),
		BridgeParticipantID: runtime.Gateway.BridgeParticipantID(platform),
		Metadata: map[string]any{
			"source":            platform,
			"platform":          platform,
			"account_uuid":      accountUUID,
			"chat_display_name": strings.TrimSpace(chat.DisplayName),
			"chat_avatar_url":   strings.TrimSpace(chat.AvatarURL),
			"chat_identity":     larkSDKChatIdentity(chat),
		},
	})
	if err != nil {
		return nil, err
	}
	senderDisplayName = strings.TrimSpace(senderDisplayName)
	messageUUID, err := runtime.Gateway.CreateMessage(ctx, sdk.CreateMessageRequest{
		WorkspaceUUID: runtime.WorkspaceUUID,
		SessionUUID:   sessionUUID,
		SenderID:      sdk.IMPersonParticipantID(platform, chat.ChatType, chat.ChatID, chat.SenderID),
		Content:       text,
		DedupeKey:     key,
		Metadata: map[string]any{
			"source":              platform,
			"platform":            platform,
			"account_uuid":        accountUUID,
			"lark_account_id":     accountUUID,
			"lark_app_id":         stringValue(account.Credential["app_id"]),
			"lark_chat_type":      chat.ChatType,
			"lark_chat_id":        chat.ChatID,
			"lark_sender_id":      chat.SenderID,
			"chat_display_name":   strings.TrimSpace(chat.DisplayName),
			"chat_avatar_url":     strings.TrimSpace(chat.AvatarURL),
			"sender_display_name": senderDisplayName,
			"lark_message_id":     hook.Event.Message.MessageID,
			"lark_root_id":        hook.Event.Message.RootID,
			"lark_parent_id":      hook.Event.Message.ParentID,
			"lark_thread_id":      hook.Event.Message.ThreadID,
			"lark_event_id":       hook.EventID(),
			"lark_message_type":   hook.Event.Message.MessageType,
			"inbound_message":     inbound,
		},
	})
	if err != nil {
		return nil, err
	}
	state.PeerSessions[stateKey] = sessionUUID
	now := time.Now().UTC()
	state.InboundSeen[key] = now.Format(time.RFC3339Nano)
	state.StreamLastEventAt = now
	state.StreamLastActivityAt = now
	if err := store.SaveAccount(ctx, state); err != nil {
		return nil, err
	}
	return &WebhookResult{Type: hook.EventType(), SessionUUID: sessionUUID, MessageUUID: messageUUID, Inbound: &inbound}, nil
}

func BuildInboundMessage(workspaceUUID, channelUUID, accountUUID string, hook *lark.Webhook, text string) sdk.InboundMessage {
	return buildInboundMessage(workspaceUUID, channelUUID, accountUUID, hook, text, larkBotIdentity{})
}

func buildInboundMessage(workspaceUUID, channelUUID, accountUUID string, hook *lark.Webhook, text string, bot larkBotIdentity) sdk.InboundMessage {
	return buildInboundMessageForPlatform(Platform, workspaceUUID, channelUUID, accountUUID, hook, text, bot)
}

func buildInboundMessageForPlatform(platform, workspaceUUID, channelUUID, accountUUID string, hook *lark.Webhook, text string, bot larkBotIdentity) sdk.InboundMessage {
	senderID := larkSenderID(hook.Event.Sender.SenderID)
	chat := hook.Event.Message.ChatIdentity(senderID)
	return buildInboundMessageWithIdentityForPlatform(platform, workspaceUUID, channelUUID, accountUUID, hook, text, bot, chat, "")
}

func buildInboundMessageWithIdentity(workspaceUUID, channelUUID, accountUUID string, hook *lark.Webhook, text string, bot larkBotIdentity, chat lark.ChatIdentity, senderDisplayName string) sdk.InboundMessage {
	return buildInboundMessageWithIdentityForPlatform(Platform, workspaceUUID, channelUUID, accountUUID, hook, text, bot, chat, senderDisplayName)
}

func buildInboundMessageWithIdentityForPlatform(platform, workspaceUUID, channelUUID, accountUUID string, hook *lark.Webhook, text string, bot larkBotIdentity, chat lark.ChatIdentity, senderDisplayName string) sdk.InboundMessage {
	mentions := larkMentionIdentities(hook.Event.Message.Mentions)
	mentionAll := larkMentionsAll(hook.Event.Message.Mentions)
	threadID := larkThreadID(hook.Event.Message)
	platform = firstString(platform, Platform)
	referenced := larkReferencedMessageFromEvent(platform, hook.Event.Message)
	return sdk.InboundMessage{
		WorkspaceUUID:     workspaceUUID,
		Platform:          platform,
		AccountUUID:       accountUUID,
		ChannelUUID:       channelUUID,
		ChatType:          chat.ChatType,
		ChatID:            chat.ChatID,
		ThreadID:          threadID,
		ChatDisplayName:   strings.TrimSpace(chat.DisplayName),
		ChatAvatarURL:     strings.TrimSpace(chat.AvatarURL),
		ChatIdentity:      larkSDKChatIdentity(chat),
		SenderID:          chat.SenderID,
		SenderDisplayName: strings.TrimSpace(senderDisplayName),
		MessageID:         hook.Event.Message.MessageID,
		Text:              text,
		ReferencedMessage: referenced,
		DedupeKey:         hook.DedupeKey(accountUUID),
		Mentions:          mentions,
		MentionedMe:       larkMentionsBot(mentions, bot),
		MentionAll:        mentionAll,
		Raw: map[string]any{
			"event_id":            hook.EventID(),
			"event_type":          hook.EventType(),
			"app_id":              hook.Header.AppID,
			"chat_id":             hook.Event.Message.ChatID,
			"chat_type":           hook.Event.Message.ChatType,
			"chat_display_name":   strings.TrimSpace(chat.DisplayName),
			"chat_avatar_url":     strings.TrimSpace(chat.AvatarURL),
			"chat_identity":       larkSDKChatIdentity(chat),
			"message_id":          hook.Event.Message.MessageID,
			"root_id":             hook.Event.Message.RootID,
			"parent_id":           hook.Event.Message.ParentID,
			"thread_id":           hook.Event.Message.ThreadID,
			"message_type":        hook.Event.Message.MessageType,
			"sender_id":           hook.Event.Sender.SenderID,
			"sender_display_name": strings.TrimSpace(senderDisplayName),
			"create_time":         hook.Event.Message.CreateTime,
			"mentions":            hook.Event.Message.Mentions,
			"mention_all":         mentionAll,
		},
	}
}

func resolveLarkReferencedMessage(ctx context.Context, runtime sdk.Runtime, account sdk.ChannelAccount, accountState *state.AccountState, platform string, message lark.EventMessage) *sdk.ReferencedMessage {
	ref := larkReferencedMessageFromEvent(platform, message)
	if ref == nil {
		return nil
	}
	if accountState == nil {
		return ref
	}
	client := clientFromAccount(runtime, account)
	seedLarkClientToken(client, accountState)
	defer captureLarkClientToken(client, accountState)
	item, err := client.Message(ctx, ref.MessageID)
	if err != nil {
		if ref.Raw == nil {
			ref.Raw = map[string]any{}
		}
		ref.Raw["fetch_error"] = err.Error()
		return ref
	}
	if item == nil {
		return ref
	}
	eventMessage := item.EventMessage()
	ref.MessageID = firstString(eventMessage.MessageID, ref.MessageID)
	ref.ChatType = firstString(eventMessage.ChatType, ref.ChatType)
	ref.ChatID = firstString(eventMessage.ChatID, ref.ChatID)
	ref.ThreadID = firstString(eventMessage.ThreadID, ref.ThreadID)
	ref.RootID = firstString(eventMessage.RootID, ref.RootID)
	ref.SenderID = larkSenderID(item.Sender.SenderID)
	ref.MessageType = strings.TrimSpace(eventMessage.MessageType)
	ref.Text = eventMessage.Text()
	ref.CreatedAt = strings.TrimSpace(eventMessage.CreateTime)
	if ref.Raw == nil {
		ref.Raw = map[string]any{}
	}
	ref.Raw["fetched"] = true
	ref.Raw["sender_type"] = strings.TrimSpace(item.Sender.SenderType)
	return ref
}

func larkReferencedMessageFromEvent(platform string, message lark.EventMessage) *sdk.ReferencedMessage {
	parentID := strings.TrimSpace(message.ParentID)
	if parentID == "" {
		return nil
	}
	return &sdk.ReferencedMessage{
		Platform:    firstString(platform, Platform),
		MessageID:   parentID,
		ChatType:    strings.TrimSpace(message.ChatType),
		ChatID:      strings.TrimSpace(message.ChatID),
		ThreadID:    strings.TrimSpace(message.ThreadID),
		RootID:      strings.TrimSpace(message.RootID),
		MessageType: strings.TrimSpace(message.MessageType),
		Raw: map[string]any{
			"parent_id": parentID,
			"root_id":   strings.TrimSpace(message.RootID),
			"thread_id": strings.TrimSpace(message.ThreadID),
		},
	}
}

func larkThreadID(message lark.EventMessage) string {
	return firstString(message.ThreadID, message.ParentID, message.RootID)
}

func larkSDKChatIdentity(chat lark.ChatIdentity) sdk.ChatIdentity {
	return sdk.ChatIdentity{
		ID:          strings.TrimSpace(chat.ChatID),
		IDType:      "chat_id",
		Type:        strings.TrimSpace(chat.ChatType),
		DisplayName: strings.TrimSpace(chat.DisplayName),
		AvatarURL:   strings.TrimSpace(chat.AvatarURL),
	}
}

func larkMentionIdentities(mentions []lark.Mention) []sdk.MentionIdentity {
	out := make([]sdk.MentionIdentity, 0, len(mentions)*3)
	for _, mention := range mentions {
		displayName := strings.TrimSpace(mention.Name)
		if larkMentionIsAll(mention) {
			if displayName == "" {
				displayName = "all"
			}
			out = append(out, sdk.MentionIdentity{ID: "all", IDType: "mention_all", DisplayName: displayName})
			continue
		}
		if id := strings.TrimSpace(mention.ID.OpenID); id != "" {
			out = append(out, sdk.MentionIdentity{ID: id, IDType: "open_id", DisplayName: displayName})
		}
		if id := strings.TrimSpace(mention.ID.UserID); id != "" {
			out = append(out, sdk.MentionIdentity{ID: id, IDType: "user_id", DisplayName: displayName})
		}
		if id := strings.TrimSpace(mention.ID.UnionID); id != "" {
			out = append(out, sdk.MentionIdentity{ID: id, IDType: "union_id", DisplayName: displayName})
		}
	}
	return uniqueMentionIdentities(out)
}

func larkMentionsAll(mentions []lark.Mention) bool {
	for _, mention := range mentions {
		if larkMentionIsAll(mention) {
			return true
		}
	}
	return false
}

func larkMessageNeedsBotName(message lark.EventMessage) bool {
	for _, mention := range message.Mentions {
		if strings.TrimSpace(mention.Name) == "" || larkMentionIsAll(mention) {
			continue
		}
		if strings.TrimSpace(mention.ID.OpenID) == "" &&
			(strings.TrimSpace(mention.ID.UserID) != "" || strings.TrimSpace(mention.ID.UnionID) != "") {
			return true
		}
	}
	return false
}

func larkMentionIsAll(mention lark.Mention) bool {
	return strings.TrimSpace(mention.Key) == "@_all"
}

type larkBotIdentity struct {
	OpenID  string
	Name    string
	UserID  string
	UnionID string
}

func larkEventOwnershipValid(account sdk.ChannelAccount, hook *lark.Webhook) bool {
	expected := strings.TrimSpace(stringValue(account.Credential["app_id"]))
	if expected == "" || hook == nil {
		return true
	}
	received := strings.TrimSpace(hook.Header.AppID)
	if received == "" {
		return true
	}
	return received == expected
}

func larkBotIdentityFromAccount(account sdk.ChannelAccount) larkBotIdentity {
	return larkBotIdentity{
		OpenID:  firstString(account.Credential["bot_open_id"], account.State["bot_open_id"], standardBotIdentityValue(account.State, "open_id")),
		Name:    firstString(account.Credential["bot_name"], account.Credential["bot_app_name"], account.State["bot_name"], account.State["bot_app_name"], standardBotIdentityDisplayName(account.State)),
		UserID:  firstString(account.Credential["bot_user_id"], account.State["bot_user_id"], standardBotIdentityValue(account.State, "user_id")),
		UnionID: firstString(account.Credential["bot_union_id"], account.State["bot_union_id"], standardBotIdentityValue(account.State, "union_id")),
	}
}

func larkBotIdentityFromAccountState(account sdk.ChannelAccount, accountState *state.AccountState) larkBotIdentity {
	if accountState == nil {
		return larkBotIdentityFromAccount(account)
	}
	return larkBotIdentity{
		OpenID:  firstString(account.Credential["bot_open_id"], accountState.BotOpenID, account.State["bot_open_id"], standardBotIdentityValue(account.State, "open_id")),
		Name:    firstString(account.Credential["bot_name"], account.Credential["bot_app_name"], accountState.BotName, account.State["bot_name"], account.State["bot_app_name"], standardBotIdentityDisplayName(account.State)),
		UserID:  firstString(account.Credential["bot_user_id"], accountState.BotUserID, account.State["bot_user_id"], standardBotIdentityValue(account.State, "user_id")),
		UnionID: firstString(account.Credential["bot_union_id"], accountState.BotUnionID, account.State["bot_union_id"], standardBotIdentityValue(account.State, "union_id")),
	}
}

func ensureLarkBotIdentity(ctx context.Context, runtime sdk.Runtime, account sdk.ChannelAccount, accountState *state.AccountState, store *connectorStateStore, needName bool) (larkBotIdentity, error) {
	bot := larkBotIdentityFromAccountState(account, accountState)
	if bot.UserID != "" || bot.UnionID != "" || (bot.OpenID != "" && (!needName || bot.Name != "")) {
		return bot, nil
	}
	if accountState == nil || store == nil {
		return bot, nil
	}
	client := clientFromAccount(runtime, account)
	now := time.Now().UTC()
	if accountState.TenantAccessToken != "" && accountState.TokenExpiresAt.After(now.Add(5*time.Minute)) {
		client.TenantToken = accountState.TenantAccessToken
		client.TenantTokenExpiresAt = accountState.TokenExpiresAt
	}
	info, err := client.BotInfo(ctx)
	if err != nil {
		if bot.OpenID != "" || bot.Name != "" || bot.UserID != "" || bot.UnionID != "" {
			return bot, nil
		}
		return bot, err
	}
	accountState.TenantAccessToken = client.TenantToken
	accountState.TokenExpiresAt = client.TenantTokenExpiresAt
	accountState.BotOpenID = strings.TrimSpace(info.Bot.OpenID)
	accountState.BotName = strings.TrimSpace(info.Bot.AppName)
	if err := store.SaveAccount(ctx, accountState); err != nil {
		return bot, err
	}
	return larkBotIdentityFromAccountState(account, accountState), nil
}

func resolveLarkDisplayNames(ctx context.Context, runtime sdk.Runtime, account sdk.ChannelAccount, accountState *state.AccountState, chat *lark.ChatIdentity, sender lark.EventSender) string {
	if accountState == nil || chat == nil {
		return ""
	}
	accountState.EnsureMaps()
	senderOpenID := strings.TrimSpace(sender.SenderID.OpenID)
	senderID := strings.TrimSpace(chat.SenderID)
	senderDisplayName := firstString(accountState.UserDisplayNames[senderID], accountState.UserDisplayNames[senderOpenID])
	if chat.ChatID != "" {
		chat.DisplayName = strings.TrimSpace(accountState.ChatDisplayNames[chat.ChatID])
	}
	if chat.DisplayName == "" && chat.ChatType == lark.ChatTypeDirect && senderDisplayName != "" {
		chat.DisplayName = senderDisplayName
		if chat.ChatID != "" {
			accountState.ChatDisplayNames[chat.ChatID] = senderDisplayName
		}
	}
	if senderDisplayName != "" && chat.DisplayName != "" {
		return senderDisplayName
	}
	client := clientFromAccount(runtime, account)
	seedLarkClientToken(client, accountState)
	defer captureLarkClientToken(client, accountState)
	if senderDisplayName == "" && strings.EqualFold(strings.TrimSpace(sender.SenderType), "user") && senderOpenID != "" {
		if info, err := client.UserInfo(ctx, senderOpenID); err == nil && info != nil {
			if name := strings.TrimSpace(info.DisplayName()); name != "" {
				senderDisplayName = name
				accountState.UserDisplayNames[senderOpenID] = name
				if senderID != "" {
					accountState.UserDisplayNames[senderID] = name
				}
			}
		}
	}
	if senderDisplayName == "" && chat.ChatType == lark.ChatTypeGroup && strings.TrimSpace(chat.ChatID) != "" && senderOpenID != "" {
		if members, err := client.ChatMembers(ctx, chat.ChatID); err == nil && members != nil {
			if name := strings.TrimSpace(members.DisplayNameForOpenID(senderOpenID)); name != "" {
				senderDisplayName = name
				accountState.UserDisplayNames[senderOpenID] = name
				if senderID != "" {
					accountState.UserDisplayNames[senderID] = name
				}
			}
		}
	}
	if chat.DisplayName == "" && chat.ChatType == lark.ChatTypeDirect && senderDisplayName != "" {
		chat.DisplayName = senderDisplayName
		if chat.ChatID != "" {
			accountState.ChatDisplayNames[chat.ChatID] = senderDisplayName
		}
	}
	if chat.DisplayName == "" && chat.ChatType == lark.ChatTypeGroup && strings.TrimSpace(chat.ChatID) != "" {
		if info, err := client.ChatInfo(ctx, chat.ChatID); err == nil && info != nil {
			if name := strings.TrimSpace(info.DisplayName()); name != "" {
				chat.DisplayName = name
				accountState.ChatDisplayNames[chat.ChatID] = name
			}
			chat.AvatarURL = strings.TrimSpace(info.AvatarURL())
		}
	}
	return senderDisplayName
}

func seedLarkClientToken(client *lark.Client, accountState *state.AccountState) {
	if client == nil || accountState == nil {
		return
	}
	if accountState.TenantAccessToken != "" && (accountState.TokenExpiresAt.IsZero() || accountState.TokenExpiresAt.After(time.Now().UTC().Add(5*time.Minute))) {
		client.TenantToken = accountState.TenantAccessToken
		client.TenantTokenExpiresAt = accountState.TokenExpiresAt
	}
}

func captureLarkClientToken(client *lark.Client, accountState *state.AccountState) {
	if client == nil || accountState == nil {
		return
	}
	if strings.TrimSpace(client.TenantToken) != "" {
		accountState.TenantAccessToken = client.TenantToken
		accountState.TokenExpiresAt = client.TenantTokenExpiresAt
	}
}

func larkSenderID(id lark.SenderID) string {
	return firstString(id.OpenID, id.UserID, id.UnionID)
}

func larkSenderMatchesBot(id lark.SenderID, bot larkBotIdentity) bool {
	return (bot.OpenID != "" && strings.TrimSpace(id.OpenID) == bot.OpenID) ||
		(bot.UserID != "" && strings.TrimSpace(id.UserID) == bot.UserID) ||
		(bot.UnionID != "" && strings.TrimSpace(id.UnionID) == bot.UnionID)
}

func larkMentionMatchesBot(mention lark.Mention, bot larkBotIdentity) bool {
	return (bot.OpenID != "" && strings.TrimSpace(mention.ID.OpenID) == bot.OpenID) ||
		(bot.Name != "" && strings.TrimSpace(mention.Name) == bot.Name) ||
		(bot.UserID != "" && strings.TrimSpace(mention.ID.UserID) == bot.UserID) ||
		(bot.UnionID != "" && strings.TrimSpace(mention.ID.UnionID) == bot.UnionID)
}

func larkMentionsBot(mentions []sdk.MentionIdentity, bot larkBotIdentity) bool {
	for _, mention := range mentions {
		id := strings.TrimSpace(mention.ID)
		switch strings.TrimSpace(mention.IDType) {
		case "open_id", "":
			if bot.OpenID != "" && id == bot.OpenID {
				return true
			}
		case "user_id":
			if bot.UserID != "" && id == bot.UserID {
				return true
			}
		case "union_id":
			if bot.UnionID != "" && id == bot.UnionID {
				return true
			}
		}
		if bot.Name != "" && strings.TrimSpace(mention.DisplayName) == bot.Name {
			return true
		}
	}
	return false
}

func uniqueMentionIdentities(mentions []sdk.MentionIdentity) []sdk.MentionIdentity {
	seen := make(map[string]struct{}, len(mentions))
	out := make([]sdk.MentionIdentity, 0, len(mentions))
	for _, mention := range mentions {
		mention.ID = strings.TrimSpace(mention.ID)
		mention.IDType = strings.TrimSpace(mention.IDType)
		mention.DisplayName = strings.TrimSpace(mention.DisplayName)
		if mention.ID == "" {
			continue
		}
		key := mention.IDType + "\x00" + mention.ID
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, mention)
	}
	return out
}

func runtimeAccountCandidates(runtime sdk.Runtime) []sdk.ChannelAccount {
	seen := make(map[string]bool)
	var out []sdk.ChannelAccount
	add := func(account sdk.ChannelAccount) {
		key := accountKey(account)
		if key == "" || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, account)
	}
	add(runtime.Account)
	for _, account := range runtime.Accounts {
		add(account)
	}
	return out
}

func selectRuntimeAccount(runtime sdk.Runtime, accountUUID string) (sdk.ChannelAccount, error) {
	accountUUID = strings.TrimSpace(accountUUID)
	candidates := runtimeAccountCandidates(runtime)
	if accountUUID != "" {
		for _, account := range candidates {
			if accountMatches(account, accountUUID) {
				return account, nil
			}
		}
		return sdk.ChannelAccount{}, fmt.Errorf("lark account %s not found in runtime", accountUUID)
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	if len(candidates) == 0 {
		return sdk.ChannelAccount{}, fmt.Errorf("lark outbound account is required")
	}
	return sdk.ChannelAccount{}, fmt.Errorf("lark outbound account is ambiguous; account_uuid is required")
}

func accountMatches(account sdk.ChannelAccount, accountID string) bool {
	return strings.TrimSpace(account.UUID) == accountID ||
		strings.TrimSpace(stringValue(account.Credential["account_id"])) == accountID ||
		strings.TrimSpace(stringValue(account.Credential["app_id"])) == accountID
}

func accountKey(account sdk.ChannelAccount) string {
	return firstString(account.UUID, account.Credential["account_id"], account.Credential["app_id"])
}

func clientFromAccount(runtime sdk.Runtime, account sdk.ChannelAccount) *lark.Client {
	baseURL := baseURLFromCredential(account.Credential)
	client := lark.NewClient(baseURL, stringValue(account.Credential["app_id"]), stringValue(account.Credential["app_secret"]))
	client.HTTPClient = runtime.HTTPClient
	return client
}

func effectiveChannelPlatform(runtime sdk.Runtime) string {
	if platform := firstString(
		runtime.Account.Platform,
		credentialPlatform(runtime.Account.Credential),
	); platform != "" {
		return platform
	}
	for _, account := range runtime.Accounts {
		if platform := firstString(account.Platform, credentialPlatform(account.Credential)); platform != "" {
			return platform
		}
	}
	return firstString(runtime.Channel.Platform, Platform)
}

func effectiveAccountPlatform(runtime sdk.Runtime, account sdk.ChannelAccount) string {
	return firstString(
		account.Platform,
		credentialPlatform(account.Credential),
		runtime.Account.Platform,
		credentialPlatform(runtime.Account.Credential),
		runtime.Channel.Platform,
		Platform,
	)
}

func credentialValidationPlatform(req sdk.CredentialValidationRequest, credential map[string]any) string {
	return firstString(req.Platform, credentialPlatform(credential), Platform)
}

func credentialPlatform(credential map[string]any) string {
	if platform := strings.TrimSpace(stringValue(credential["platform"])); platform != "" {
		return platform
	}
	switch strings.ToLower(strings.TrimSpace(stringValue(credential["brand"]))) {
	case "feishu":
		return "feishu"
	case "lark":
		return "lark"
	}
	baseURL := strings.ToLower(strings.TrimSpace(stringValue(credential["base_url"])))
	switch {
	case strings.Contains(baseURL, "open.feishu.cn"):
		return "feishu"
	case strings.Contains(baseURL, "open.larksuite.com"):
		return "lark"
	default:
		return ""
	}
}

func baseURLFromCredential(credential map[string]any) string {
	if strings.EqualFold(strings.TrimSpace(stringValue(credential["brand"])), "lark") {
		return lark.DefaultLarkBaseURL
	}
	return lark.DefaultBaseURL
}

func receiveIDType(req sdk.OutboundMessage) string {
	if value := strings.TrimSpace(stringValue(req.Raw["receive_id_type"])); value != "" {
		return value
	}
	if req.ChatType == sdk.ChatTypeGroup {
		return "chat_id"
	}
	return lark.ReceiveIDTypeForTarget(req.ChatID)
}

func larkAckWantsReaction(req sdk.OutboundAck) bool {
	action := strings.ToLower(strings.TrimSpace(firstString(req.Action, req.Raw["action"])))
	return action == "" || action == "start" || action == "processing"
}

func larkUnsupportedAckMode(req sdk.OutboundAck) string {
	mode := strings.ToLower(strings.TrimSpace(firstString(req.Mode, req.Raw["mode"])))
	if mode == "" || mode == "auto" || mode == "reaction" {
		return ""
	}
	return mode
}

func larkAckTargetMessageID(req sdk.OutboundAck) string {
	return firstString(req.TargetMessageID, req.Raw["target_message_id"], req.Raw["message_id"], req.Raw["lark_message_id"])
}

func larkAckEmojiType(req sdk.OutboundAck) string {
	value := strings.TrimSpace(firstString(req.Emoji, req.Raw["emoji"], req.Raw["emoji_type"]))
	switch strings.ToLower(value) {
	case "", "thinking", "think", "processing":
		return "THINKING"
	case "typing":
		return "Typing"
	case "onit", "on_it", "on-it":
		return "OnIt"
	case "one_second", "one-second", "onesecond", "wait":
		return "OneSecond"
	case "ok":
		return "OK"
	case "done", "check", "checkmark", "check_mark":
		return "DONE"
	case "thumbsup", "thumbs_up", "thumbs-up", "+1":
		return "THUMBSUP"
	case "smile":
		return "SMILE"
	default:
		return value
	}
}

func outboundMsgType(req sdk.OutboundMessage) string {
	if value := strings.TrimSpace(stringValue(req.Raw["msg_type"])); value != "" {
		if strings.EqualFold(value, "markdown") || strings.EqualFold(value, "md") {
			return "post"
		}
		return value
	}
	if isMarkdownOutbound(req) {
		return "post"
	}
	return "text"
}

func outboundContent(req sdk.OutboundMessage, msgType string) (string, error) {
	if raw := req.Raw["content"]; raw != nil {
		switch typed := raw.(type) {
		case string:
			if strings.TrimSpace(typed) != "" {
				return typed, nil
			}
		default:
			data, err := json.Marshal(typed)
			if err != nil {
				return "", fmt.Errorf("encode lark outbound content: %w", err)
			}
			return string(data), nil
		}
	}
	if msgType == "post" && isMarkdownOutbound(req) {
		return larkMarkdownPostContent(req)
	}
	if msgType != "text" {
		return "", fmt.Errorf("lark outbound content is required for msg_type=%s", msgType)
	}
	data, err := json.Marshal(map[string]string{"text": larkOutboundMentionText(req)})
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func isMarkdownOutbound(req sdk.OutboundMessage) bool {
	format := strings.ToLower(strings.TrimSpace(firstString(
		req.Format,
		req.Raw["format"],
		req.Raw["content_type"],
		req.Raw["content_format"],
		req.Raw["contentType"],
		req.Raw["contentFormat"],
		req.Raw["message_format"],
		req.Raw["messageFormat"],
		req.Raw["msg_format"],
		req.Raw["msgFormat"],
		req.Raw["msg_type"],
		req.Raw["msgType"],
	)))
	return format == "markdown" || format == "md" || format == "text/markdown" || format == "application/markdown"
}

func larkMarkdownPostContent(req sdk.OutboundMessage) (string, error) {
	text := larkOutboundMentionText(req)
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("text is required")
	}
	zhCN := map[string]any{
		"content": [][]map[string]string{
			{{"tag": "md", "text": text}},
		},
	}
	if title := larkOutboundTitle(req); title != "" {
		zhCN["title"] = title
	}
	data, err := json.Marshal(map[string]any{
		"zh_cn": zhCN,
	})
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func larkOutboundTitle(req sdk.OutboundMessage) string {
	if title := strings.TrimSpace(firstString(req.Title, req.Raw["title"])); title != "" {
		return title
	}
	return ""
}

func larkOutboundMentionText(req sdk.OutboundMessage) string {
	text := strings.TrimSpace(req.Text)
	var tags []string
	if req.MentionAll || boolValue(req.Raw["mention_all"]) || boolValue(req.Raw["mentionAll"]) ||
		boolValue(req.Raw["at_all"]) || boolValue(req.Raw["atAll"]) || boolValue(req.Raw["isAtAll"]) {
		tags = append(tags, `<at user_id="all">Everyone</at>`)
	}
	for _, id := range stringSlice(firstValue(req.Raw["mention_ids"], req.Raw["mentionIds"], req.Raw["at_user_ids"], req.Raw["atUserIds"], req.Raw["user_ids"], req.Raw["userIds"])) {
		tags = append(tags, fmt.Sprintf(`<at user_id="%s">%s</at>`, escapeLarkAtText(id), escapeLarkAtText(id)))
	}
	mentions := append([]sdk.MentionIdentity{}, req.Mentions...)
	mentions = append(mentions, rawMentionIdentities(req.Raw["mentions"])...)
	for _, mention := range mentions {
		id := strings.TrimSpace(mention.ID)
		if id == "" {
			continue
		}
		if strings.EqualFold(id, "all") || strings.EqualFold(strings.TrimSpace(mention.IDType), "all") ||
			strings.EqualFold(strings.TrimSpace(mention.IDType), "mention_all") {
			tags = append(tags, `<at user_id="all">Everyone</at>`)
			continue
		}
		name := strings.TrimSpace(mention.DisplayName)
		if name == "" {
			name = id
		}
		tags = append(tags, fmt.Sprintf(`<at user_id="%s">%s</at>`, escapeLarkAtText(id), escapeLarkAtText(name)))
	}
	tags = uniqueStrings(tags)
	if len(tags) == 0 {
		return text
	}
	prefix := strings.Join(tags, " ")
	if text == "" {
		return prefix
	}
	return prefix + "\n" + text
}

func escapeLarkAtText(value string) string {
	value = strings.ReplaceAll(value, "&", "&amp;")
	value = strings.ReplaceAll(value, `"`, "&quot;")
	value = strings.ReplaceAll(value, "<", "&lt;")
	value = strings.ReplaceAll(value, ">", "&gt;")
	return value
}

func rawMentionIdentities(value any) []sdk.MentionIdentity {
	switch typed := value.(type) {
	case []sdk.MentionIdentity:
		return typed
	case []any:
		out := make([]sdk.MentionIdentity, 0, len(typed))
		for _, item := range typed {
			out = append(out, mentionIdentityFromAny(item))
		}
		return out
	case []map[string]any:
		out := make([]sdk.MentionIdentity, 0, len(typed))
		for _, item := range typed {
			out = append(out, mentionIdentityFromAny(item))
		}
		return out
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil
		}
		var parsed []map[string]any
		if err := json.Unmarshal([]byte(typed), &parsed); err == nil {
			out := make([]sdk.MentionIdentity, 0, len(parsed))
			for _, item := range parsed {
				out = append(out, mentionIdentityFromAny(item))
			}
			return out
		}
	case json.RawMessage:
		var parsed []map[string]any
		if err := json.Unmarshal(typed, &parsed); err == nil {
			out := make([]sdk.MentionIdentity, 0, len(parsed))
			for _, item := range parsed {
				out = append(out, mentionIdentityFromAny(item))
			}
			return out
		}
	}
	return nil
}

func mentionIdentityFromAny(value any) sdk.MentionIdentity {
	mention, ok := value.(sdk.MentionIdentity)
	if ok {
		return mention
	}
	item, ok := value.(map[string]any)
	if !ok {
		return sdk.MentionIdentity{}
	}
	return sdk.MentionIdentity{
		ID:          firstString(item["id"], item["ID"], item["open_id"], item["openId"], item["user_id"], item["userId"], item["union_id"], item["unionId"]),
		IDType:      firstString(item["id_type"], item["idType"], item["IDType"], item["type"]),
		DisplayName: firstString(item["display_name"], item["displayName"], item["name"]),
	}
}

func stringSlice(value any) []string {
	var values []any
	switch typed := value.(type) {
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if item = strings.TrimSpace(item); item != "" {
				out = append(out, item)
			}
		}
		return out
	case []any:
		values = typed
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil
		}
		var parsed []any
		if err := json.Unmarshal([]byte(typed), &parsed); err == nil {
			values = parsed
			break
		}
		return []string{strings.TrimSpace(typed)}
	case json.RawMessage:
		var parsed []any
		if err := json.Unmarshal(typed, &parsed); err == nil {
			values = parsed
		}
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if item := strings.TrimSpace(stringValue(value)); item != "" {
			out = append(out, item)
		}
	}
	return uniqueStrings(out)
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func firstValue(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func cloneMap(value map[string]any) map[string]any {
	out := make(map[string]any, len(value))
	for key, item := range value {
		out[key] = item
	}
	return out
}

func credentialValidationFailure(credential, state map[string]any, platform string, err error) *sdk.CredentialValidationResult {
	message := ""
	if err != nil {
		message = err.Error()
	}
	return &sdk.CredentialValidationResult{
		Valid:       false,
		AccountKey:  firstString(credential["account_id"], credential["app_id"]),
		DisplayName: firstString(credential["display_name"], credential["bot_name"], credential["app_id"]),
		Credential:  credential,
		State:       state,
		Metadata:    map[string]any{"platform": firstString(platform, Platform)},
		Error:       message,
	}
}

func optionalBool(value any) *bool {
	if value == nil {
		return nil
	}
	out := boolValue(value)
	return &out
}

type connectorStateStore struct {
	mu           sync.Mutex
	accounts     map[string]*state.AccountState
	sdkAccounts  map[string]sdk.ChannelAccount
	accountStore sdk.AccountStore
}

func newConnectorStateStore(accountStore sdk.AccountStore) *connectorStateStore {
	return &connectorStateStore{
		accounts:     make(map[string]*state.AccountState),
		sdkAccounts:  make(map[string]sdk.ChannelAccount),
		accountStore: accountStore,
	}
}

func (s *connectorStateStore) seed(account sdk.ChannelAccount) {
	accountID := accountKey(account)
	if accountID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state := sdkAccountToState(account)
	s.accounts[accountID] = &state
	s.sdkAccounts[accountID] = account
}

func (s *connectorStateStore) LoadAccount(ctx context.Context, accountID string) (*state.AccountState, error) {
	s.mu.Lock()
	if account, ok := s.accounts[accountID]; ok {
		sdkAccount := s.sdkAccounts[accountID]
		accountStore := s.accountStore
		s.mu.Unlock()
		if refreshed, ok, err := loadAccountState(ctx, accountStore, sdkAccount); err != nil {
			return nil, err
		} else if ok {
			s.mu.Lock()
			s.accounts[accountID] = refreshed
			sdkAccount.State = accountStateToSDK(*refreshed, sdkAccount).State
			s.sdkAccounts[accountID] = sdkAccount
			s.mu.Unlock()
			return refreshed, nil
		}
		return account, nil
	}
	accountStore := s.accountStore
	s.mu.Unlock()
	if refreshed, ok, err := loadAccountState(ctx, accountStore, sdk.ChannelAccount{UUID: accountID}); err != nil {
		return nil, err
	} else if ok {
		s.mu.Lock()
		s.accounts[accountID] = refreshed
		s.sdkAccounts[accountID] = accountStateToSDK(*refreshed, sdk.ChannelAccount{UUID: accountID})
		s.mu.Unlock()
		return refreshed, nil
	}
	account := &state.AccountState{AccountID: accountID}
	account.EnsureMaps()
	s.mu.Lock()
	s.accounts[accountID] = account
	s.mu.Unlock()
	return account, nil
}

func loadAccountState(ctx context.Context, accountStore sdk.AccountStore, account sdk.ChannelAccount) (*state.AccountState, bool, error) {
	if accountStore == nil || strings.TrimSpace(account.UUID) == "" {
		return nil, false, nil
	}
	stateMap, err := accountStore.LoadChannelAccountState(ctx, account.UUID)
	if err != nil {
		return nil, false, err
	}
	if len(stateMap) == 0 {
		return nil, false, nil
	}
	account.State = stateMap
	refreshed := sdkAccountToState(account)
	return &refreshed, true, nil
}

func (s *connectorStateStore) SaveAccount(ctx context.Context, account *state.AccountState) error {
	if err := state.TouchAccount(account); err != nil {
		return err
	}
	s.mu.Lock()
	s.accounts[account.AccountID] = account
	existing := s.sdkAccounts[account.AccountID]
	sdkAccount := accountStateToSDK(*account, existing)
	s.sdkAccounts[account.AccountID] = sdkAccount
	accountStore := s.accountStore
	s.mu.Unlock()
	if accountStore != nil && sdkAccount.UUID != "" {
		return accountStore.SaveChannelAccountState(ctx, sdkAccount.UUID, sdkAccount.State)
	}
	return nil
}

func sdkAccountToState(account sdk.ChannelAccount) state.AccountState {
	out := state.AccountState{
		AccountID:                  accountKey(account),
		AppID:                      stringValue(account.Credential["app_id"]),
		BaseURL:                    baseURLFromCredential(account.Credential),
		Brand:                      stringValue(account.Credential["brand"]),
		TenantAccessToken:          stringValue(account.State["tenant_access_token"]),
		TokenExpiresAt:             timeValue(account.State["tenant_access_token_expires_at"]),
		BotOpenID:                  firstString(account.Credential["bot_open_id"], account.State["bot_open_id"], standardBotIdentityValue(account.State, "open_id")),
		BotName:                    firstString(account.Credential["bot_name"], account.Credential["bot_app_name"], account.State["bot_name"], account.State["bot_app_name"], standardBotIdentityDisplayName(account.State)),
		BotUserID:                  firstString(account.Credential["bot_user_id"], account.State["bot_user_id"], standardBotIdentityValue(account.State, "user_id")),
		BotUnionID:                 firstString(account.Credential["bot_union_id"], account.State["bot_union_id"], standardBotIdentityValue(account.State, "union_id")),
		ChannelLinkSession:         stringValue(account.State["channel_link_session"]),
		PeerSessions:               stringMap(account.State["peer_sessions"]),
		UserDisplayNames:           stringMap(account.State["user_display_names"]),
		ChatDisplayNames:           stringMap(account.State["chat_display_names"]),
		InboundSeen:                stringMap(account.State["inbound_seen"]),
		SentBeakMessages:           stringMap(account.State["sent_beak_messages"]),
		StreamCursors:              stringMap(account.State["stream_cursors"]),
		StreamConnectionState:      stringValue(account.State[sdk.RuntimeHealthKeyStreamConnectionState]),
		StreamConnectedAt:          timeValue(account.State[sdk.RuntimeHealthKeyStreamConnectedAt]),
		StreamDisconnectedAt:       timeValue(account.State[sdk.RuntimeHealthKeyStreamDisconnectedAt]),
		StreamLastActivityAt:       timeValue(account.State[sdk.RuntimeHealthKeyStreamLastActivityAt]),
		StreamLastPingAt:           timeValue(account.State[sdk.RuntimeHealthKeyStreamLastPingAt]),
		StreamLastPongAt:           timeValue(account.State[sdk.RuntimeHealthKeyStreamLastPongAt]),
		StreamLastEventAt:          timeValue(account.State[sdk.RuntimeHealthKeyStreamLastEventAt]),
		StreamLastError:            stringValue(account.State[sdk.RuntimeHealthKeyStreamLastError]),
		StreamLastErrorAt:          timeValue(account.State[sdk.RuntimeHealthKeyStreamLastErrorAt]),
		StreamReconnectRequestedAt: timeValue(account.State[sdk.RuntimeHealthKeyStreamReconnectRequestedAt]),
		StreamReconnectError:       stringValue(account.State[sdk.RuntimeHealthKeyStreamReconnectError]),
		StreamReconnectErrorAt:     timeValue(account.State[sdk.RuntimeHealthKeyStreamReconnectErrorAt]),
		StreamSessionExpired:       boolValue(account.State[sdk.RuntimeHealthKeyStreamSessionExpired]),
	}
	out.EnsureMaps()
	return out
}

func accountStateToSDK(account state.AccountState, existing sdk.ChannelAccount) sdk.ChannelAccount {
	if existing.UUID == "" {
		existing.UUID = account.AccountID
	}
	if strings.TrimSpace(existing.Platform) == "" {
		existing.Platform = firstString(credentialPlatform(existing.Credential), credentialPlatform(map[string]any{"brand": account.Brand, "base_url": account.BaseURL}), Platform)
	}
	if existing.Credential == nil {
		existing.Credential = map[string]any{}
	}
	existing.State = map[string]any{
		"channel_link_session":                         account.ChannelLinkSession,
		"peer_sessions":                                account.PeerSessions,
		"user_display_names":                           account.UserDisplayNames,
		"chat_display_names":                           account.ChatDisplayNames,
		"inbound_seen":                                 account.InboundSeen,
		"sent_beak_messages":                           account.SentBeakMessages,
		"stream_cursors":                               account.StreamCursors,
		"tenant_access_token":                          account.TenantAccessToken,
		"tenant_access_token_expires_at":               account.TokenExpiresAt,
		"bot_open_id":                                  account.BotOpenID,
		"bot_name":                                     account.BotName,
		"bot_user_id":                                  account.BotUserID,
		"bot_union_id":                                 account.BotUnionID,
		sdk.RuntimeHealthKeyStreamConnectionState:      account.StreamConnectionState,
		sdk.RuntimeHealthKeyStreamConnectedAt:          account.StreamConnectedAt,
		sdk.RuntimeHealthKeyStreamDisconnectedAt:       account.StreamDisconnectedAt,
		sdk.RuntimeHealthKeyStreamLastActivityAt:       account.StreamLastActivityAt,
		sdk.RuntimeHealthKeyStreamLastPingAt:           account.StreamLastPingAt,
		sdk.RuntimeHealthKeyStreamLastPongAt:           account.StreamLastPongAt,
		sdk.RuntimeHealthKeyStreamLastEventAt:          account.StreamLastEventAt,
		sdk.RuntimeHealthKeyStreamLastError:            account.StreamLastError,
		sdk.RuntimeHealthKeyStreamLastErrorAt:          account.StreamLastErrorAt,
		sdk.RuntimeHealthKeyStreamReconnectRequestedAt: account.StreamReconnectRequestedAt,
		sdk.RuntimeHealthKeyStreamReconnectError:       account.StreamReconnectError,
		sdk.RuntimeHealthKeyStreamReconnectErrorAt:     account.StreamReconnectErrorAt,
		sdk.RuntimeHealthKeyStreamSessionExpired:       account.StreamSessionExpired,
		"updated_at":                                   account.UpdatedAt,
	}
	if identities := larkBotIdentityState(larkBotIdentity{
		OpenID:  account.BotOpenID,
		Name:    account.BotName,
		UserID:  account.BotUserID,
		UnionID: account.BotUnionID,
	}); len(identities) > 0 {
		existing.State["bot_identities"] = identities
		existing.State["bot_identity"] = identities[0]
	}
	return existing
}

func larkBotIdentityState(bot larkBotIdentity) []map[string]any {
	var identities []map[string]any
	add := func(id, idType string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		identity := map[string]any{
			"id":      id,
			"id_type": idType,
		}
		if name := strings.TrimSpace(bot.Name); name != "" {
			identity["display_name"] = name
		}
		identities = append(identities, identity)
	}
	add(bot.OpenID, "open_id")
	add(bot.UserID, "user_id")
	add(bot.UnionID, "union_id")
	return identities
}

func standardBotIdentityValue(state map[string]any, idTypes ...string) string {
	wanted := make(map[string]struct{}, len(idTypes))
	for _, idType := range idTypes {
		idType = strings.TrimSpace(idType)
		if idType != "" {
			wanted[idType] = struct{}{}
		}
	}
	for _, identity := range standardBotIdentityMaps(state) {
		idType := strings.TrimSpace(stringValue(identity["id_type"]))
		if len(wanted) > 0 {
			if _, ok := wanted[idType]; !ok {
				continue
			}
		}
		if id := strings.TrimSpace(stringValue(identity["id"])); id != "" {
			return id
		}
	}
	return ""
}

func standardBotIdentityDisplayName(state map[string]any) string {
	for _, identity := range standardBotIdentityMaps(state) {
		if name := firstString(identity["display_name"], identity["displayName"], identity["name"]); name != "" {
			return name
		}
	}
	return ""
}

func standardBotIdentityMaps(state map[string]any) []map[string]any {
	if len(state) == 0 {
		return nil
	}
	var out []map[string]any
	out = append(out, botIdentityMapsFromAny(state["bot_identities"])...)
	out = append(out, botIdentityMapsFromAny(state["bot_identity"])...)
	return out
}

func botIdentityMapsFromAny(value any) []map[string]any {
	switch typed := value.(type) {
	case nil:
		return nil
	case map[string]any:
		return []map[string]any{typed}
	case []map[string]any:
		return typed
	case []any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, botIdentityMapsFromAny(item)...)
		}
		return out
	case json.RawMessage:
		var list []map[string]any
		if err := json.Unmarshal(typed, &list); err == nil {
			return list
		}
		var item map[string]any
		if err := json.Unmarshal(typed, &item); err == nil {
			return []map[string]any{item}
		}
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil
		}
		return botIdentityMapsFromAny(json.RawMessage(typed))
	}
	return nil
}

func timeValue(value any) time.Time {
	switch typed := value.(type) {
	case time.Time:
		return typed
	case string:
		if typed == "" {
			return time.Time{}
		}
		parsed, _ := time.Parse(time.RFC3339Nano, typed)
		return parsed
	case json.RawMessage:
		var text string
		if err := json.Unmarshal(typed, &text); err == nil {
			return timeValue(text)
		}
	}
	return time.Time{}
}

func stringMap(value any) map[string]string {
	out := make(map[string]string)
	switch typed := value.(type) {
	case map[string]string:
		for key, item := range typed {
			out[key] = item
		}
	case map[string]any:
		for key, item := range typed {
			if stringItem, ok := item.(string); ok {
				out[key] = stringItem
			}
		}
	case json.RawMessage:
		_ = json.Unmarshal(typed, &out)
	}
	return out
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	default:
		return fmt.Sprint(typed)
	}
}

func boolValue(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true")
	default:
		return false
	}
}

func firstString(values ...any) string {
	for _, value := range values {
		if stringValue := strings.TrimSpace(stringValue(value)); stringValue != "" {
			return stringValue
		}
	}
	return ""
}

var _ sdk.Connector = Connector{}
var _ WebhookConnector = Connector{}
var _ WebhookRequestConnector = Connector{}
