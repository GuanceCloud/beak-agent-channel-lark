package beaklark

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/GuanceCloud/beak-agent-channel-lark/sdk"
)

func TestLarkScenarioMultipleAccountsShareGroupButUseSeparateSessions(t *testing.T) {
	connector := NewConnector()
	webhookConnector, ok := any(connector).(WebhookConnector)
	if !ok {
		t.Fatal("connector should implement WebhookConnector")
	}
	gateway := newScenarioGateway(Platform)
	store := newScenarioStore()
	accountA := scenarioLarkAccount("account-a", "cli_a", "secret_a", "ou_bot_a")
	accountB := scenarioLarkAccount("account-b", "cli_b", "secret_b", "ou_bot_b")
	runtime := sdk.Runtime{
		WorkspaceUUID: "workspace-1",
		Channel:       sdk.Channel{UUID: "channel-1", WorkspaceUUID: "workspace-1", Platform: Platform},
		Accounts:      []sdk.ChannelAccount{accountA, accountB},
		Gateway:       gateway,
		AccountStore:  store,
	}

	err := connector.Start(context.Background(), runtime)
	if err != nil {
		t.Fatalf("Start error=%v", err)
	}
	if gateway.linkSession("account-a") == "" || gateway.linkSession("account-b") == "" {
		t.Fatalf("missing channel link sessions: %+v", gateway.linkSessions)
	}

	resultA1, err := webhookConnector.HandleWebhook(context.Background(), runtime, accountA, []byte(`{
		"schema":"2.0",
		"header":{"event_id":"evt-a-1","event_type":"im.message.receive_v1","app_id":"cli_a","token":"verify-token"},
		"event":{
			"sender":{"sender_id":{"open_id":"ou_user_a"},"sender_type":"user"},
			"message":{"message_id":"om-a-1","chat_id":"oc_group","chat_type":"group","message_type":"text","content":"{\"text\":\"hello from bot a group\"}","create_time":"1770000000000"}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	accountA.State = store.state("account-a")
	runtime.Accounts[0] = accountA

	resultA2, err := webhookConnector.HandleWebhook(context.Background(), runtime, accountA, []byte(`{
		"schema":"2.0",
		"header":{"event_id":"evt-a-2","event_type":"im.message.receive_v1","app_id":"cli_a","token":"verify-token"},
		"event":{
			"sender":{"sender_id":{"open_id":"ou_user_b"},"sender_type":"user"},
			"message":{"message_id":"om-a-2","chat_id":"oc_group","chat_type":"group","message_type":"text","content":"{\"text\":\"second group message\"}","create_time":"1770000000001"}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if resultA1.SessionUUID == "" || resultA1.SessionUUID != resultA2.SessionUUID {
		t.Fatalf("same account and group should reuse session: first=%+v second=%+v", resultA1, resultA2)
	}

	resultB, err := webhookConnector.HandleWebhook(context.Background(), runtime, accountB, []byte(`{
		"schema":"2.0",
		"header":{"event_id":"evt-b-1","event_type":"im.message.receive_v1","app_id":"cli_b","token":"verify-token"},
		"event":{
			"sender":{"sender_id":{"open_id":"ou_user_c"},"sender_type":"user"},
			"message":{"message_id":"om-b-1","chat_id":"oc_group","chat_type":"group","message_type":"text","content":"{\"text\":\"hello from bot b group\"}","create_time":"1770000000002"}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if resultB.SessionUUID == "" || resultB.SessionUUID == resultA1.SessionUUID {
		t.Fatalf("different accounts in same group need separate sessions: accountA=%s accountB=%s", resultA1.SessionUUID, resultB.SessionUUID)
	}

	accountA.State = store.state("account-a")
	runtime.Accounts[0] = accountA
	duplicate, err := webhookConnector.HandleWebhook(context.Background(), runtime, accountA, []byte(`{
		"schema":"2.0",
		"header":{"event_id":"evt-a-1","event_type":"im.message.receive_v1","app_id":"cli_a","token":"verify-token"},
		"event":{
			"sender":{"sender_id":{"open_id":"ou_user_a"},"sender_type":"user"},
			"message":{"message_id":"om-a-1","chat_id":"oc_group","chat_type":"group","message_type":"text","content":"{\"text\":\"hello from bot a group\"}","create_time":"1770000000000"}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if !duplicate.Ignored || duplicate.Reason != "duplicate" || duplicate.SessionUUID != resultA1.SessionUUID {
		t.Fatalf("duplicate should be ignored with original session: %+v", duplicate)
	}

	selfEcho, err := webhookConnector.HandleWebhook(context.Background(), runtime, accountA, []byte(`{
		"schema":"2.0",
		"header":{"event_id":"evt-self","event_type":"im.message.receive_v1","app_id":"cli_a","token":"verify-token"},
		"event":{
			"sender":{"sender_id":{"open_id":"ou_bot_a"},"sender_type":"user"},
			"message":{"message_id":"om-self","chat_id":"oc_group","chat_type":"group","message_type":"text","content":"{\"text\":\"self echo\"}","create_time":"1770000000003"}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if !selfEcho.Ignored || selfEcho.Reason != "self_echo" {
		t.Fatalf("self echo should be ignored: %+v", selfEcho)
	}

	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	if len(gateway.messages) != 3 {
		t.Fatalf("expected three non-duplicate user messages, got %+v", gateway.messages)
	}
	if gateway.messages[0].SenderID != "im:lark:group:oc_group:user:ou_user_a" {
		t.Fatalf("sender participant=%q", gateway.messages[0].SenderID)
	}
}

func TestLarkScenarioOutboundGroupAndDirectRequests(t *testing.T) {
	var sentPaths []string
	httpClient := &http.Client{Transport: scenarioRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		sentPaths = append(sentPaths, r.URL.Path)
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["app_id"] != "cli_b" || body["app_secret"] != "secret_b" {
				t.Fatalf("token body=%+v", body)
			}
			return scenarioJSONResponse(map[string]any{"code": 0, "msg": "ok", "tenant_access_token": "token-b", "expire": 7200})
		case "/open-apis/im/v1/messages":
			if got := r.Header.Get("Authorization"); got != "Bearer token-b" {
				t.Fatalf("authorization=%q", got)
			}
			var body struct {
				ReceiveID string `json:"receive_id"`
				MsgType   string `json:"msg_type"`
				Content   string `json:"content"`
				UUID      string `json:"uuid"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			var content map[string]string
			if err := json.Unmarshal([]byte(body.Content), &content); err != nil {
				t.Fatal(err)
			}
			switch body.UUID {
			case "message-group":
				if got := r.URL.Query().Get("receive_id_type"); got != "chat_id" {
					t.Fatalf("group receive_id_type=%q", got)
				}
				if body.ReceiveID != "oc_group" || body.MsgType != "text" || content["text"] != "group reply" {
					t.Fatalf("group body=%+v content=%+v", body, content)
				}
				return scenarioJSONResponse(map[string]any{"code": 0, "msg": "ok", "data": map[string]any{"message_id": "om-group", "chat_id": "oc_group"}})
			case "message-direct":
				if got := r.URL.Query().Get("receive_id_type"); got != "open_id" {
					t.Fatalf("direct receive_id_type=%q", got)
				}
				if body.ReceiveID != "ou_direct" || body.MsgType != "text" || content["text"] != "direct reply" {
					t.Fatalf("direct body=%+v content=%+v", body, content)
				}
				return scenarioJSONResponse(map[string]any{"code": 0, "msg": "ok", "data": map[string]any{"message_id": "om-direct", "chat_id": "oc_direct"}})
			default:
				t.Fatalf("unexpected message uuid: %+v", body)
			}
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
		return nil, nil
	})}

	connector := NewConnector()
	runtime := sdk.Runtime{
		HTTPClient: httpClient,
		Accounts: []sdk.ChannelAccount{
			scenarioLarkAccount("account-a", "cli_a", "secret_a", "ou_bot_a"),
			scenarioLarkAccount("account-b", "cli_b", "secret_b", "ou_bot_b"),
		},
	}

	groupResult, err := connector.Send(context.Background(), runtime, sdk.OutboundMessage{
		AccountUUID: "account-b",
		ChatType:    sdk.ChatTypeGroup,
		ChatID:      "oc_group",
		Text:        "group reply",
		MessageUUID: "message-group",
	})
	if err != nil {
		t.Fatal(err)
	}
	if groupResult.MessageID != "om-group" || groupResult.AccountUUID != "account-b" {
		t.Fatalf("group result=%+v", groupResult)
	}

	directResult, err := connector.Send(context.Background(), runtime, sdk.OutboundMessage{
		AccountUUID: "account-b",
		ChatType:    sdk.ChatTypeDirect,
		ChatID:      "ou_direct",
		Text:        "direct reply",
		MessageUUID: "message-direct",
	})
	if err != nil {
		t.Fatal(err)
	}
	if directResult.MessageID != "om-direct" || directResult.AccountUUID != "account-b" {
		t.Fatalf("direct result=%+v", directResult)
	}
	if strings.Join(sentPaths, ",") != "/open-apis/auth/v3/tenant_access_token/internal,/open-apis/im/v1/messages,/open-apis/auth/v3/tenant_access_token/internal,/open-apis/im/v1/messages" {
		t.Fatalf("request paths=%+v", sentPaths)
	}
}

func TestLarkScenarioCredentialInboundAndFixedReply(t *testing.T) {
	const fixedReply = "Beak Agent 已收到你的飞书消息"
	connector := NewConnector()
	webhookConnector, ok := any(connector).(WebhookConnector)
	if !ok {
		t.Fatal("connector should implement WebhookConnector")
	}
	gateway := newScenarioGateway(Platform)
	store := newScenarioStore()
	account := scenarioLarkAccount("account-fixed", "cli_fixed", "secret_fixed", "ou_bot_fixed")

	var sent scenarioLarkSentMessage
	httpClient := &http.Client{Transport: scenarioRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["app_id"] != "cli_fixed" || body["app_secret"] != "secret_fixed" {
				t.Fatalf("token body=%+v", body)
			}
			return scenarioJSONResponse(map[string]any{"code": 0, "msg": "ok", "tenant_access_token": "token-fixed", "expire": 7200})
		case "/open-apis/im/v1/messages":
			if got := r.Header.Get("Authorization"); got != "Bearer token-fixed" {
				t.Fatalf("authorization=%q", got)
			}
			var body struct {
				ReceiveID string `json:"receive_id"`
				MsgType   string `json:"msg_type"`
				Content   string `json:"content"`
				UUID      string `json:"uuid"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			var content map[string]string
			if err := json.Unmarshal([]byte(body.Content), &content); err != nil {
				t.Fatal(err)
			}
			sent = scenarioLarkSentMessage{
				receiveIDType: r.URL.Query().Get("receive_id_type"),
				receiveID:     body.ReceiveID,
				msgType:       body.MsgType,
				uuid:          body.UUID,
				text:          content["text"],
			}
			return scenarioJSONResponse(map[string]any{"code": 0, "msg": "ok", "data": map[string]any{"message_id": "om-fixed-reply", "chat_id": "oc_fixed"}})
		case "/open-apis/contact/v3/users/ou_user_fixed", "/open-apis/im/v1/chats/oc_fixed":
			return scenarioJSONResponse(map[string]any{"code": 99991663, "msg": "permission denied"})
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
		return nil, nil
	})}

	runtime := sdk.Runtime{
		WorkspaceUUID: "workspace-1",
		Channel:       sdk.Channel{UUID: "channel-1", WorkspaceUUID: "workspace-1", Platform: Platform},
		Account:       account,
		Gateway:       gateway,
		AccountStore:  store,
		HTTPClient:    httpClient,
	}

	err := connector.Start(context.Background(), runtime)
	if err != nil {
		t.Fatalf("Start error=%v", err)
	}

	result, err := webhookConnector.HandleWebhook(context.Background(), runtime, account, []byte(`{
		"schema":"2.0",
		"header":{"event_id":"evt-fixed-1","event_type":"im.message.receive_v1","app_id":"cli_fixed","token":"verify-token"},
		"event":{
			"sender":{"sender_id":{"open_id":"ou_user_fixed"},"sender_type":"user"},
			"message":{"message_id":"om-fixed-1","chat_id":"oc_fixed","chat_type":"group","message_type":"text","content":"{\"text\":\"你好 Beak\"}","create_time":"1770000000100"}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.Ignored || result.SessionUUID == "" || result.MessageUUID == "" {
		t.Fatalf("result=%+v", result)
	}
	if result.Inbound == nil || result.Inbound.ChatType != sdk.ChatTypeGroup || result.Inbound.ChatID != "oc_fixed" || result.Inbound.Text != "你好 Beak" {
		t.Fatalf("inbound=%+v", result.Inbound)
	}

	account.State = store.state("account-fixed")
	sendResult, err := connector.Send(context.Background(), runtime, sdk.OutboundMessage{
		AccountUUID: "account-fixed",
		ChatType:    result.Inbound.ChatType,
		ChatID:      result.Inbound.ChatID,
		Text:        fixedReply,
		MessageUUID: "agent-message-fixed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if sendResult.MessageID != "om-fixed-reply" || sendResult.AccountUUID != "account-fixed" {
		t.Fatalf("send result=%+v", sendResult)
	}
	if sent.receiveIDType != "chat_id" || sent.receiveID != "oc_fixed" || sent.msgType != "text" || sent.uuid != "agent-message-fixed" || sent.text != fixedReply {
		t.Fatalf("sent=%+v", sent)
	}

	gateway.mu.Lock()
	createdMessages := append([]sdk.CreateMessageRequest(nil), gateway.messages...)
	chatRequests := append([]sdk.EnsureChatSessionRequest(nil), gateway.chatRequests...)
	gateway.mu.Unlock()
	if len(createdMessages) != 1 || createdMessages[0].Content != "你好 Beak" || createdMessages[0].SenderID != "im:lark:group:oc_fixed:user:ou_user_fixed" {
		t.Fatalf("created messages=%+v", createdMessages)
	}
	if len(chatRequests) != 1 || chatRequests[0].AccountUUID != "account-fixed" || chatRequests[0].ChatType != sdk.ChatTypeGroup || chatRequests[0].ChatID != "oc_fixed" {
		t.Fatalf("chat requests=%+v", chatRequests)
	}
	state := store.state("account-fixed")
	peerSessions, ok := state["peer_sessions"].(map[string]string)
	if !ok || peerSessions["group:oc_fixed"] != result.SessionUUID {
		t.Fatalf("peer sessions=%+v", state["peer_sessions"])
	}
	inboundSeen, ok := state["inbound_seen"].(map[string]string)
	if !ok || inboundSeen["account-fixed:message:om-fixed-1"] == "" {
		t.Fatalf("inbound seen=%+v", state["inbound_seen"])
	}
}

func TestLarkScenarioMentionsInboundAndOutbound(t *testing.T) {
	connector := NewConnector()
	webhookConnector, ok := any(connector).(WebhookConnector)
	if !ok {
		t.Fatal("connector should implement WebhookConnector")
	}
	gateway := newScenarioGateway(Platform)
	store := newScenarioStore()
	account := scenarioLarkAccount("account-mention", "cli_mention", "secret_mention", "ou_bot_mention")
	var sent scenarioLarkSentMessage
	httpClient := &http.Client{Transport: scenarioRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			return scenarioJSONResponse(map[string]any{"code": 0, "msg": "ok", "tenant_access_token": "token-mention", "expire": 7200})
		case "/open-apis/im/v1/messages":
			var body struct {
				ReceiveID string `json:"receive_id"`
				MsgType   string `json:"msg_type"`
				Content   string `json:"content"`
				UUID      string `json:"uuid"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			var content map[string]string
			if err := json.Unmarshal([]byte(body.Content), &content); err != nil {
				t.Fatal(err)
			}
			sent = scenarioLarkSentMessage{
				receiveIDType: r.URL.Query().Get("receive_id_type"),
				receiveID:     body.ReceiveID,
				msgType:       body.MsgType,
				uuid:          body.UUID,
				text:          content["text"],
			}
			return scenarioJSONResponse(map[string]any{"code": 0, "msg": "ok", "data": map[string]any{"message_id": "om-mention-reply", "chat_id": "oc_mentions"}})
		case "/open-apis/contact/v3/users/ou_user_mention", "/open-apis/im/v1/chats/oc_mentions":
			return scenarioJSONResponse(map[string]any{"code": 99991663, "msg": "permission denied"})
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
		return nil, nil
	})}
	runtime := sdk.Runtime{
		WorkspaceUUID: "workspace-1",
		Channel:       sdk.Channel{UUID: "channel-1", WorkspaceUUID: "workspace-1", Platform: Platform},
		Account:       account,
		Gateway:       gateway,
		AccountStore:  store,
		HTTPClient:    httpClient,
	}
	if err := connector.Start(context.Background(), runtime); err != nil {
		t.Fatalf("Start error=%v", err)
	}
	result, err := webhookConnector.HandleWebhook(context.Background(), runtime, account, []byte(`{
		"schema":"2.0",
		"header":{"event_id":"evt-mention","event_type":"im.message.receive_v1","app_id":"cli_mention","token":"verify-token"},
		"event":{
			"sender":{"sender_id":{"open_id":"ou_user_mention"},"sender_type":"user"},
			"message":{"message_id":"om-mention","chat_id":"oc_mentions","chat_type":"group","message_type":"text","content":"{\"text\":\"@_user_1 @所有人 帮我看下\"}","create_time":"1770000000200","mentions":[{"key":"@_user_1","id":{"open_id":"ou_bot_mention","user_id":"bot_user_mention"},"name":"Beak Bot"},{"key":"@_all","id":{},"name":"所有人"}]}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.Inbound == nil || !result.Inbound.MentionedMe || !result.Inbound.MentionAll || len(result.Inbound.Mentions) != 3 {
		t.Fatalf("inbound mention state=%+v", result.Inbound)
	}

	account.State = store.state("account-mention")
	runtime.Account = account
	sendResult, err := connector.Send(context.Background(), runtime, sdk.OutboundMessage{
		AccountUUID: "account-mention",
		ChatType:    sdk.ChatTypeGroup,
		ChatID:      "oc_mentions",
		Text:        "收到，我来处理",
		MessageUUID: "agent-message-mention",
		MentionAll:  true,
		Mentions: []sdk.MentionIdentity{
			{ID: "ou_user_mention", IDType: "open_id", DisplayName: "Alice"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantText := `<at user_id="all">Everyone</at> <at user_id="ou_user_mention">Alice</at>` + "\n收到，我来处理"
	if sendResult.MessageID != "om-mention-reply" || sent.receiveIDType != "chat_id" || sent.receiveID != "oc_mentions" || sent.text != wantText {
		t.Fatalf("send result=%+v sent=%+v want text=%q", sendResult, sent, wantText)
	}
}

func TestLarkScenarioWebSocketEventDirectReply(t *testing.T) {
	connector := NewConnector()
	eventConnector, ok := any(connector).(EventConnector)
	if !ok {
		t.Fatal("connector should implement EventConnector")
	}
	gateway := newScenarioGateway(Platform)
	store := newScenarioStore()
	account := scenarioLarkAccount("account-ws", "cli_ws", "secret_ws", "ou_bot_ws")
	var sent struct {
		msgType       string
		uuid          string
		text          string
		replyInThread bool
	}
	httpClient := &http.Client{Transport: scenarioRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["app_id"] != "cli_ws" || body["app_secret"] != "secret_ws" {
				t.Fatalf("token body=%+v", body)
			}
			return scenarioJSONResponse(map[string]any{"code": 0, "msg": "ok", "tenant_access_token": "token-ws", "expire": 7200})
		case "/open-apis/im/v1/messages/om-direct-1/reply":
			if got := r.Header.Get("Authorization"); got != "Bearer token-ws" {
				t.Fatalf("authorization=%q", got)
			}
			var body struct {
				MsgType       string `json:"msg_type"`
				Content       string `json:"content"`
				UUID          string `json:"uuid"`
				ReplyInThread bool   `json:"reply_in_thread"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			var content map[string]string
			if err := json.Unmarshal([]byte(body.Content), &content); err != nil {
				t.Fatal(err)
			}
			sent.msgType = body.MsgType
			sent.uuid = body.UUID
			sent.text = content["text"]
			sent.replyInThread = body.ReplyInThread
			return scenarioJSONResponse(map[string]any{"code": 0, "msg": "ok", "data": map[string]any{"message_id": "om-direct-reply", "chat_id": "oc_direct"}})
		case "/open-apis/contact/v3/users/ou_direct_user":
			return scenarioJSONResponse(map[string]any{"code": 99991663, "msg": "permission denied"})
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
		return nil, nil
	})}
	runtime := sdk.Runtime{
		WorkspaceUUID: "workspace-1",
		Channel:       sdk.Channel{UUID: "channel-1", WorkspaceUUID: "workspace-1", Platform: Platform},
		Account:       account,
		Gateway:       gateway,
		AccountStore:  store,
		HTTPClient:    httpClient,
	}
	if err := connector.Start(context.Background(), runtime); err != nil {
		t.Fatalf("Start error=%v", err)
	}
	result, err := eventConnector.HandleEvent(context.Background(), runtime, account, []byte(`{
		"app_id":"cli_ws",
		"event_id":"evt-ws-direct-1",
		"event_type":"im.message.receive_v1",
		"sender":{"sender_id":{"open_id":"ou_direct_user"},"sender_type":"user"},
		"message":{"message_id":"om-direct-1","chat_type":"p2p","message_type":"text","content":"{\"text\":\"direct websocket\"}","create_time":"1770000000300"}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.Inbound == nil || result.Inbound.ChatType != sdk.ChatTypeDirect || result.Inbound.ChatID != "ou_direct_user" || result.Inbound.SenderID != "ou_direct_user" {
		t.Fatalf("inbound=%+v", result.Inbound)
	}
	gateway.mu.Lock()
	created := append([]sdk.CreateMessageRequest(nil), gateway.messages...)
	gateway.mu.Unlock()
	if len(created) != 1 || created[0].SenderID != "im:lark:direct:ou_direct_user:user:ou_direct_user" || created[0].Content != "direct websocket" {
		t.Fatalf("created messages=%+v", created)
	}

	account.State = store.state("account-ws")
	runtime.Account = account
	sendResult, err := connector.Send(context.Background(), runtime, sdk.OutboundMessage{
		AccountUUID: "account-ws",
		ChatType:    result.Inbound.ChatType,
		ChatID:      result.Inbound.ChatID,
		Text:        "direct reply",
		MessageUUID: "agent-message-direct",
		Raw: map[string]any{
			"reply_to_message_id": "om-direct-1",
			"reply_in_thread":     true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if sendResult.MessageID != "om-direct-reply" || sent.msgType != "text" || sent.uuid != "agent-message-direct" || sent.text != "direct reply" || !sent.replyInThread {
		t.Fatalf("send result=%+v sent=%+v", sendResult, sent)
	}
}

func scenarioLarkAccount(uuid, appID, secret, botOpenID string) sdk.ChannelAccount {
	return sdk.ChannelAccount{
		UUID:          uuid,
		WorkspaceUUID: "workspace-1",
		ChannelUUID:   "channel-1",
		Platform:      Platform,
		Credential: map[string]any{
			"account_id":         uuid,
			"app_id":             appID,
			"app_secret":         secret,
			"verification_token": "verify-token",
			"brand":              "feishu",
			"base_url":           "https://open.feishu.test",
			"bot_open_id":        botOpenID,
		},
		State: map[string]any{},
	}
}

type scenarioLarkSentMessage struct {
	receiveIDType string
	receiveID     string
	msgType       string
	uuid          string
	text          string
}

type scenarioGateway struct {
	mu              sync.Mutex
	platform        string
	linkSessions    map[string]string
	chatSessions    map[string]string
	chatRequests    []sdk.EnsureChatSessionRequest
	messages        []sdk.CreateMessageRequest
	nextChatSession int
	nextMessage     int
}

func newScenarioGateway(platform string) *scenarioGateway {
	return &scenarioGateway{
		platform:     platform,
		linkSessions: make(map[string]string),
		chatSessions: make(map[string]string),
	}
}

func (g *scenarioGateway) EnsureChannel(ctx context.Context, req sdk.EnsureChannelRequest) (string, error) {
	if req.Platform != g.platform {
		return "", errors.New("unexpected platform")
	}
	return "channel-1", nil
}

func (g *scenarioGateway) EnsureChannelLinkSession(ctx context.Context, req sdk.EnsureChannelLinkSessionRequest) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.linkSessions[req.AccountUUID] == "" {
		g.linkSessions[req.AccountUUID] = "link-" + req.AccountUUID
	}
	return g.linkSessions[req.AccountUUID], nil
}

func (g *scenarioGateway) EnsureChatSession(ctx context.Context, req sdk.EnsureChatSessionRequest) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.chatRequests = append(g.chatRequests, req)
	key := req.AccountUUID + ":" + req.ChatType + ":" + req.ChatID
	if g.chatSessions[key] == "" {
		g.nextChatSession++
		g.chatSessions[key] = "session-" + req.AccountUUID + "-" + req.ChatType + "-" + req.ChatID
	}
	return g.chatSessions[key], nil
}

func (g *scenarioGateway) CreateMessage(ctx context.Context, req sdk.CreateMessageRequest) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.nextMessage++
	g.messages = append(g.messages, req)
	return "message-scenario-" + req.SessionUUID, nil
}

func (g *scenarioGateway) StreamSession(ctx context.Context, req sdk.StreamSessionRequest, handle func(sdk.StreamEvent) error) error {
	return nil
}

func (g *scenarioGateway) AgentParticipantID() string {
	return "agent:agent-1"
}

func (g *scenarioGateway) BridgeParticipantID(platform string) string {
	return sdk.BridgeParticipantID(platform)
}

func (g *scenarioGateway) linkSession(accountUUID string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.linkSessions[accountUUID]
}

type scenarioStore struct {
	mu     sync.Mutex
	states map[string]map[string]any
}

func newScenarioStore() *scenarioStore {
	return &scenarioStore{states: make(map[string]map[string]any)}
}

func (s *scenarioStore) SaveChannelAccountState(ctx context.Context, accountUUID string, state map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	copied := make(map[string]any, len(state))
	for key, value := range state {
		copied[key] = value
	}
	s.states[accountUUID] = copied
	return nil
}

func (s *scenarioStore) LoadChannelAccountState(ctx context.Context, accountUUID string) (map[string]any, error) {
	return s.state(accountUUID), nil
}

func (s *scenarioStore) state(accountUUID string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	copied := make(map[string]any, len(s.states[accountUUID]))
	for key, value := range s.states[accountUUID] {
		copied[key] = value
	}
	return copied
}

type scenarioRoundTripFunc func(*http.Request) (*http.Response, error)

func (f scenarioRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func scenarioJSONResponse(body any) (*http.Response, error) {
	var builder strings.Builder
	if err := json.NewEncoder(&builder).Encode(body); err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(builder.String())),
	}, nil
}
