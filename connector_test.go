package beaklark

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GuanceCloud/beak-agent-channel-lark/sdk"
)

func TestLarkConnectorMetadataAndSchema(t *testing.T) {
	var connector sdk.Connector = NewConnector()
	if _, ok := connector.(EventConnector); !ok {
		t.Fatal("NewConnector should expose EventConnector for host-owned Lark WebSocket routing")
	}
	if _, ok := connector.(WebhookConnector); !ok {
		t.Fatal("NewConnector should expose WebhookConnector for HTTP callback compatibility")
	}
	if _, ok := connector.(WebhookRequestConnector); !ok {
		t.Fatal("NewConnector should expose WebhookRequestConnector for signed HTTP callback compatibility")
	}

	metadata := connector.Metadata()
	if metadata.ID != ID || metadata.Platform != Platform || metadata.Label != "Lark/Feishu" {
		t.Fatalf("metadata=%+v", metadata)
	}
	if !metadata.Capabilities.Text || !metadata.Capabilities.DirectChat || !metadata.Capabilities.GroupChat || metadata.Capabilities.Media {
		t.Fatalf("capabilities=%+v", metadata.Capabilities)
	}
	if !metadata.Capabilities.Stream || metadata.Capabilities.Webhook {
		t.Fatalf("stream/webhook capabilities=%+v", metadata.Capabilities)
	}
	if len(metadata.Capabilities.LoginModes) != 1 || metadata.Capabilities.LoginModes[0] != sdk.LoginModeCredential {
		t.Fatalf("login modes=%+v", metadata.Capabilities.LoginModes)
	}

	schema := connector.CredentialSchema(context.Background())
	if schema.Type != "object" || schema.AdditionalProperties {
		t.Fatalf("schema=%+v", schema)
	}
	if len(schema.LoginModes) != 1 || schema.LoginModes[0] != sdk.LoginModeCredential {
		t.Fatalf("schema login modes=%+v", schema.LoginModes)
	}
	if _, ok := schema.Properties["app_id"]; !ok {
		t.Fatalf("missing app_id schema=%+v", schema.Properties)
	}
	if !schema.Properties["app_secret"].Secret {
		t.Fatalf("app_secret should be secret")
	}
	if _, ok := schema.Properties["base_url"]; ok {
		t.Fatalf("base_url must not be exposed in credential schema")
	}
}

func TestLarkConnectorValidateCredentialFetchesTokenAndBotInfo(t *testing.T) {
	var sawToken bool
	var sawBotInfo bool
	httpClient := &http.Client{Transport: testRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			sawToken = true
			if req.Method != http.MethodPost {
				t.Fatalf("token method=%s", req.Method)
			}
			var body map[string]string
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["app_id"] != "cli_validate" || body["app_secret"] != "secret_validate" {
				t.Fatalf("token body=%+v", body)
			}
			return testJSONResponse(map[string]any{"code": 0, "tenant_access_token": "tenant-token-1", "expire": 3600})
		case "/open-apis/bot/v3/info":
			sawBotInfo = true
			if got := req.Header.Get("Authorization"); got != "Bearer tenant-token-1" {
				t.Fatalf("Authorization=%q", got)
			}
			return testJSONResponse(map[string]any{
				"code": 0,
				"bot": map[string]any{
					"open_id":         "ou_validate_bot",
					"app_name":        "Beak Validate Bot",
					"activate_status": 1,
					"avatar_url":      "https://example.test/avatar.png",
				},
			})
		default:
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
			return nil, nil
		}
	})}

	result, err := NewConnector().ValidateCredential(context.Background(), sdk.CredentialValidationRequest{
		Credential: map[string]any{
			"app_id":     "cli_validate",
			"app_secret": "secret_validate",
			"brand":      "feishu",
		},
		Runtime: sdk.Runtime{HTTPClient: httpClient},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sawToken || !sawBotInfo {
		t.Fatalf("sawToken=%v sawBotInfo=%v", sawToken, sawBotInfo)
	}
	if !result.Valid || result.AccountKey != "cli_validate" || result.DisplayName != "Beak Validate Bot" {
		t.Fatalf("result=%+v", result)
	}
	if result.Credential["bot_open_id"] != "ou_validate_bot" || result.State["tenant_access_token"] != "tenant-token-1" {
		t.Fatalf("credential=%+v state=%+v", result.Credential, result.State)
	}
	identity, ok := result.State["bot_identity"].(map[string]any)
	if !ok || identity["id"] != "ou_validate_bot" || identity["id_type"] != "open_id" || identity["display_name"] != "Beak Validate Bot" {
		t.Fatalf("bot_identity=%+v state=%+v", result.State["bot_identity"], result.State)
	}
	if result.Metadata["activate_status"] != 1 {
		t.Fatalf("metadata=%+v", result.Metadata)
	}
}

func TestLarkConnectorValidateCredentialReturnsInvalidOnTokenFailure(t *testing.T) {
	httpClient := &http.Client{Transport: testRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/open-apis/auth/v3/tenant_access_token/internal" {
			t.Fatalf("unexpected request: %s", req.URL.Path)
		}
		return testJSONResponse(map[string]any{"code": 999, "msg": "bad secret"})
	})}

	result, err := NewConnector().ValidateCredential(context.Background(), sdk.CredentialValidationRequest{
		Credential: map[string]any{"app_id": "cli_bad", "app_secret": "bad"},
		Runtime:    sdk.Runtime{HTTPClient: httpClient},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Valid || result.AccountKey != "cli_bad" || !strings.Contains(result.Error, "bad secret") {
		t.Fatalf("result=%+v", result)
	}
}

func newTestEventConnector(t *testing.T) EventConnector {
	t.Helper()
	connector, ok := NewConnector().(EventConnector)
	if !ok {
		t.Fatal("NewConnector should expose EventConnector")
	}
	return connector
}

func newTestWebhookConnector(t *testing.T) WebhookConnector {
	t.Helper()
	connector, ok := NewConnector().(WebhookConnector)
	if !ok {
		t.Fatal("NewConnector should expose WebhookConnector")
	}
	return connector
}

func newTestWebhookRequestConnector(t *testing.T) WebhookRequestConnector {
	t.Helper()
	connector, ok := NewConnector().(WebhookRequestConnector)
	if !ok {
		t.Fatal("NewConnector should expose WebhookRequestConnector")
	}
	return connector
}

func TestLarkConnectorStartEnsuresChannelLink(t *testing.T) {
	connector := NewConnector()
	gateway := &fakeSDKGateway{}
	store := newFakeSDKAccountStore()
	err := connector.Start(context.Background(), sdk.Runtime{
		WorkspaceUUID: "workspace-1",
		Channel:       sdk.Channel{UUID: "channel-1", WorkspaceUUID: "workspace-1", Platform: Platform},
		Account:       sdkAccount("account-1", "cli_1", "secret_1", ""),
		Gateway:       gateway,
		AccountStore:  store,
	})
	if err != nil {
		t.Fatalf("Start error=%v", err)
	}
	if gateway.channelPlatform != Platform {
		t.Fatalf("channel platform=%q", gateway.channelPlatform)
	}
	if gateway.channelLinkAccountUUID != "account-1" {
		t.Fatalf("channel link account=%q", gateway.channelLinkAccountUUID)
	}
	if state := store.state("account-1"); state["channel_link_session"] != "link-account-1" {
		t.Fatalf("state=%+v", state)
	}
}

func TestLarkConnectorWebhookChallenge(t *testing.T) {
	connector := newTestWebhookConnector(t)
	result, err := connector.HandleWebhook(context.Background(), sdk.Runtime{}, sdkAccount("account-1", "cli_1", "secret_1", ""), []byte(`{"type":"url_verification","challenge":"challenge-1","token":"verify-token"}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.Challenge != "challenge-1" || result.Type != "url_verification" {
		t.Fatalf("result=%+v", result)
	}
}

func TestLarkConnectorEncryptedWebhookChallenge(t *testing.T) {
	connector := newTestWebhookConnector(t)
	account := sdkAccount("account-1", "cli_1", "secret_1", "")
	account.Credential["encrypt_key"] = "encrypt-key"
	plain := `{"type":"url_verification","challenge":"challenge-1","token":"verify-token"}`
	body := []byte(`{"encrypt":"` + encryptWebhookForTest(t, plain, "encrypt-key") + `"}`)
	result, err := connector.HandleWebhook(context.Background(), sdk.Runtime{}, account, body)
	if err != nil {
		t.Fatal(err)
	}
	if result.Challenge != "challenge-1" || result.Type != "url_verification" {
		t.Fatalf("result=%+v", result)
	}
}

func TestLarkConnectorWebhookRequestVerifiesSignature(t *testing.T) {
	connector := newTestWebhookRequestConnector(t)
	account := sdkAccount("account-1", "cli_1", "secret_1", "")
	account.Credential["encrypt_key"] = "encrypt-key"
	body := []byte(`{"type":"url_verification","challenge":"challenge-1","token":"verify-token"}`)
	req, err := http.NewRequest(http.MethodPost, "https://beak.test/webhook", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	signLarkWebhookRequest(req, body, "encrypt-key")
	response, err := connector.HandleWebhookRequest(context.Background(), sdk.Runtime{}, account, req)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", response.StatusCode)
	}
	var payload map[string]string
	if err := json.Unmarshal(response.Body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["challenge"] != "challenge-1" || len(payload) != 1 {
		t.Fatalf("payload=%+v", payload)
	}
}

func TestLarkConnectorWebhookRequestRejectsMissingSignature(t *testing.T) {
	connector := newTestWebhookRequestConnector(t)
	account := sdkAccount("account-1", "cli_1", "secret_1", "")
	account.Credential["encrypt_key"] = "encrypt-key"
	body := []byte(`{"type":"url_verification","challenge":"challenge-1","token":"verify-token"}`)
	req, err := http.NewRequest(http.MethodPost, "https://beak.test/webhook", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connector.HandleWebhookRequest(context.Background(), sdk.Runtime{}, account, req); err == nil || !strings.Contains(err.Error(), "signature headers are required") {
		t.Fatalf("HandleWebhookRequest() error=%v, want missing signature", err)
	}
}

func TestLarkConnectorWebhookRequestRejectsExpiredSignature(t *testing.T) {
	connector := newTestWebhookRequestConnector(t)
	account := sdkAccount("account-1", "cli_1", "secret_1", "")
	account.Credential["encrypt_key"] = "encrypt-key"
	body := []byte(`{"type":"url_verification","challenge":"challenge-1","token":"verify-token"}`)
	req, err := http.NewRequest(http.MethodPost, "https://beak.test/webhook", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	signLarkWebhookRequestAt(req, body, "encrypt-key", time.Now().UTC().Add(-2*time.Hour))
	if _, err := connector.HandleWebhookRequest(context.Background(), sdk.Runtime{}, account, req); err == nil || !strings.Contains(err.Error(), "timestamp is expired") {
		t.Fatalf("HandleWebhookRequest() error=%v, want expired timestamp", err)
	}
}

func TestLarkConnectorWebhookRequestReturnsSuccessAck(t *testing.T) {
	connector := newTestWebhookRequestConnector(t)
	gateway := &fakeSDKGateway{}
	store := newFakeSDKAccountStore()
	account := sdkAccount("account-1", "cli_1", "secret_1", "")
	account.Credential["encrypt_key"] = "encrypt-key"
	body := []byte(`{
		"schema":"2.0",
		"header":{"event_id":"evt_ack","event_type":"im.message.receive_v1","app_id":"cli_1","token":"verify-token"},
		"event":{
			"sender":{"sender_id":{"open_id":"ou_user"},"sender_type":"user"},
			"message":{"message_id":"om_ack","chat_id":"oc_group","chat_type":"group","message_type":"text","content":"{\"text\":\"hello\"}","create_time":"1770000000000"}
		}
	}`)
	req, err := http.NewRequest(http.MethodPost, "https://beak.test/webhook", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	signLarkWebhookRequest(req, body, "encrypt-key")
	response, err := connector.HandleWebhookRequest(context.Background(), sdk.Runtime{
		WorkspaceUUID: "workspace-1",
		Channel:       sdk.Channel{UUID: "channel-1", WorkspaceUUID: "workspace-1", Platform: Platform},
		Account:       account,
		Gateway:       gateway,
		AccountStore:  store,
	}, account, req)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", response.StatusCode)
	}
	var payload map[string]string
	if err := json.Unmarshal(response.Body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["msg"] != "success" || len(payload) != 1 {
		t.Fatalf("payload=%+v", payload)
	}
}

func TestLarkConnectorWebhookCreatesMessageAndDedupes(t *testing.T) {
	connector := newTestWebhookConnector(t)
	gateway := &fakeSDKGateway{}
	store := newFakeSDKAccountStore()
	account := sdkAccount("account-1", "cli_1", "secret_1", "")
	body := []byte(`{
		"schema":"2.0",
		"header":{"event_id":"evt_1","event_type":"im.message.receive_v1","app_id":"cli_1","token":"verify-token"},
		"event":{
			"sender":{"sender_id":{"open_id":"ou_user"},"sender_type":"user"},
			"message":{"message_id":"om_1","chat_id":"oc_group","chat_type":"group","message_type":"text","content":"{\"text\":\"hello group\"}","create_time":"1770000000000","mentions":[{"key":"@_user_1","id":{"open_id":"ou_bot"},"name":"Beak Bot"}]}
		}
	}`)
	runtime := sdk.Runtime{
		WorkspaceUUID: "workspace-1",
		Channel:       sdk.Channel{UUID: "channel-1", WorkspaceUUID: "workspace-1", Platform: Platform},
		Account:       account,
		Gateway:       gateway,
		AccountStore:  store,
	}
	result, err := connector.HandleWebhook(context.Background(), runtime, account, body)
	if err != nil {
		t.Fatal(err)
	}
	if result.Ignored || result.SessionUUID != "session-1" || result.MessageUUID != "message-1" {
		t.Fatalf("result=%+v", result)
	}
	if result.Inbound == nil || result.Inbound.ChatType != sdk.ChatTypeGroup || result.Inbound.ChatID != "oc_group" || result.Inbound.AccountUUID != "account-1" {
		t.Fatalf("inbound=%+v", result.Inbound)
	}
	if !result.Inbound.MentionedMe || len(result.Inbound.Mentions) != 1 || result.Inbound.Mentions[0].ID != "ou_bot" || result.Inbound.Mentions[0].IDType != "open_id" {
		t.Fatalf("inbound mentions=%+v mentioned_me=%v", result.Inbound.Mentions, result.Inbound.MentionedMe)
	}
	gateway.mu.Lock()
	if len(gateway.chatSessions) != 1 {
		t.Fatalf("chatSessions=%+v", gateway.chatSessions)
	}
	chatReq := gateway.chatSessions[0]
	if chatReq.AccountUUID != "account-1" || chatReq.ChatType != sdk.ChatTypeGroup || chatReq.ChatID != "oc_group" || chatReq.SenderID != "ou_user" {
		t.Fatalf("chatReq=%+v", chatReq)
	}
	if len(gateway.messages) != 1 {
		t.Fatalf("messages=%+v", gateway.messages)
	}
	if gateway.messages[0].SenderID != "im:lark:group:oc_group:user:ou_user" || gateway.messages[0].Content != "hello group" {
		t.Fatalf("message=%+v", gateway.messages[0])
	}
	gateway.mu.Unlock()

	account.State = store.state("account-1")
	runtime.Account = account
	duplicate, err := connector.HandleWebhook(context.Background(), runtime, account, body)
	if err != nil {
		t.Fatal(err)
	}
	if !duplicate.Ignored || duplicate.Reason != "duplicate" {
		t.Fatalf("duplicate=%+v", duplicate)
	}
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	if len(gateway.messages) != 1 {
		t.Fatalf("duplicate created message=%+v", gateway.messages)
	}
	if gateway.messages[0].DedupeKey != "account-1:message:om_1" {
		t.Fatalf("dedupe key=%q", gateway.messages[0].DedupeKey)
	}
}

func TestLarkConnectorWebhookCreatesMessageFromPostEnvelope(t *testing.T) {
	connector := newTestWebhookConnector(t)
	gateway := &fakeSDKGateway{}
	store := newFakeSDKAccountStore()
	account := sdkAccount("account-1", "cli_1", "secret_1", "")
	body := []byte(`{
		"schema":"2.0",
		"header":{"event_id":"evt_post","event_type":"im.message.receive_v1","app_id":"cli_1","token":"verify-token"},
		"event":{
			"sender":{"sender_id":{"open_id":"ou_user"},"sender_type":"user"},
			"message":{"message_id":"om_post","chat_id":"oc_group","chat_type":"group","message_type":"post","content":"{\"post\":{\"zh_cn\":{\"title\":\"公告\",\"content\":[[{\"tag\":\"text\",\"text\":\"第一行\"}],[{\"tag\":\"text\",\"text\":\"第二行\"}]]}}}","create_time":"1770000000000"}
		}
	}`)
	result, err := connector.HandleWebhook(context.Background(), sdk.Runtime{
		WorkspaceUUID: "workspace-1",
		Channel:       sdk.Channel{UUID: "channel-1", WorkspaceUUID: "workspace-1", Platform: Platform},
		Account:       account,
		Gateway:       gateway,
		AccountStore:  store,
	}, account, body)
	if err != nil {
		t.Fatal(err)
	}
	if result.Ignored || result.Inbound == nil {
		t.Fatalf("result=%+v", result)
	}
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	if len(gateway.messages) != 1 || gateway.messages[0].Content != "**公告**\n\n第一行\n第二行" {
		t.Fatalf("messages=%+v", gateway.messages)
	}
}

func TestLarkConnectorHandleEventAcceptsWebSocketEvent(t *testing.T) {
	connector := newTestEventConnector(t)
	gateway := &fakeSDKGateway{}
	store := newFakeSDKAccountStore()
	account := sdkAccount("account-1", "cli_1", "secret_1", "")
	body := []byte(`{
		"app_id":"cli_1",
		"event_id":"evt_1",
		"event_type":"im.message.receive_v1",
		"sender":{"sender_id":{"open_id":"ou_user"},"sender_type":"user"},
		"message":{"message_id":"om_1","chat_id":"oc_group","chat_type":"group","message_type":"text","content":"{\"text\":\"hello websocket\"}","create_time":"1770000000000"}
	}`)
	result, err := connector.HandleEvent(context.Background(), sdk.Runtime{
		WorkspaceUUID: "workspace-1",
		Channel:       sdk.Channel{UUID: "channel-1", WorkspaceUUID: "workspace-1", Platform: Platform},
		Account:       account,
		Gateway:       gateway,
		AccountStore:  store,
	}, account, body)
	if err != nil {
		t.Fatal(err)
	}
	if result.Ignored || result.Type != "im.message.receive_v1" || result.MessageUUID != "message-1" {
		t.Fatalf("result=%+v", result)
	}
	if len(gateway.messages) != 1 || gateway.messages[0].Content != "hello websocket" {
		t.Fatalf("messages=%+v", gateway.messages)
	}
}

func TestLarkConnectorHandleEventAcceptsFlatEventWithTypeAndUserID(t *testing.T) {
	connector := newTestEventConnector(t)
	gateway := &fakeSDKGateway{}
	account := sdkAccount("account-1", "cli_1", "secret_1", "")
	body := []byte(`{
		"type":"im.message.receive_v1",
		"app_id":"cli_1",
		"event_id":"evt_user_id",
		"sender":{"sender_id":{"user_id":"user_direct"},"sender_type":"user"},
		"message":{"message_id":"om_user_id","chat_type":"p2p","message_type":"text","content":"{\"text\":\"hello user id\"}","create_time":"1770000000000"}
	}`)
	result, err := connector.HandleEvent(context.Background(), sdk.Runtime{
		WorkspaceUUID: "workspace-1",
		Channel:       sdk.Channel{UUID: "channel-1", WorkspaceUUID: "workspace-1", Platform: Platform},
		Account:       account,
		Gateway:       gateway,
		AccountStore:  newFakeSDKAccountStore(),
	}, account, body)
	if err != nil {
		t.Fatal(err)
	}
	if result.Ignored || result.Type != "im.message.receive_v1" || result.Inbound == nil || result.Inbound.ChatID != "user_direct" || result.Inbound.SenderID != "user_direct" {
		t.Fatalf("result=%+v inbound=%+v", result, result.Inbound)
	}
	if len(gateway.messages) != 1 || gateway.messages[0].Content != "hello user id" {
		t.Fatalf("messages=%+v", gateway.messages)
	}
}

func TestLarkConnectorHandleEventUsesLoadedBotIdentity(t *testing.T) {
	connector := newTestEventConnector(t)
	gateway := &fakeSDKGateway{}
	store := newFakeSDKAccountStore()
	account := sdkAccount("account-1", "cli_1", "secret_1", "")
	delete(account.Credential, "bot_open_id")
	if err := store.SaveChannelAccountState(context.Background(), "account-1", map[string]any{
		"bot_open_id": "ou_bot_loaded",
	}); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{
		"app_id":"cli_1",
		"event_id":"evt_self_loaded",
		"event_type":"im.message.receive_v1",
		"sender":{"sender_id":{"open_id":"ou_bot_loaded"},"sender_type":"user"},
		"message":{"message_id":"om_self_loaded","chat_id":"oc_group","chat_type":"group","message_type":"text","content":"{\"text\":\"self echo\"}","create_time":"1770000000000"}
	}`)
	result, err := connector.HandleEvent(context.Background(), sdk.Runtime{
		WorkspaceUUID: "workspace-1",
		Channel:       sdk.Channel{UUID: "channel-1", WorkspaceUUID: "workspace-1", Platform: Platform},
		Account:       account,
		Gateway:       gateway,
		AccountStore:  store,
	}, account, body)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !result.Ignored || result.Reason != "self_echo" {
		t.Fatalf("result=%+v", result)
	}
	if len(gateway.messages) != 0 {
		t.Fatalf("messages=%+v", gateway.messages)
	}
}

func TestLarkConnectorHandleEventUsesStandardBotIdentityState(t *testing.T) {
	connector := newTestEventConnector(t)
	gateway := &fakeSDKGateway{}
	store := newFakeSDKAccountStore()
	account := sdkAccount("account-1", "cli_1", "secret_1", "")
	delete(account.Credential, "bot_open_id")
	if err := store.SaveChannelAccountState(context.Background(), "account-1", map[string]any{
		"bot_identities": []map[string]any{
			{"id": "ou_bot_standard", "id_type": "open_id", "display_name": "Beak Bot"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{
		"app_id":"cli_1",
		"event_id":"evt_self_standard",
		"event_type":"im.message.receive_v1",
		"sender":{"sender_id":{"open_id":"ou_bot_standard"},"sender_type":"user"},
		"message":{"message_id":"om_self_standard","chat_id":"oc_group","chat_type":"group","message_type":"text","content":"{\"text\":\"self echo\"}","create_time":"1770000000000"}
	}`)
	result, err := connector.HandleEvent(context.Background(), sdk.Runtime{
		WorkspaceUUID: "workspace-1",
		Channel:       sdk.Channel{UUID: "channel-1", WorkspaceUUID: "workspace-1", Platform: Platform},
		Account:       account,
		Gateway:       gateway,
		AccountStore:  store,
	}, account, body)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !result.Ignored || result.Reason != "self_echo" {
		t.Fatalf("result=%+v", result)
	}
	if len(gateway.messages) != 0 {
		t.Fatalf("messages=%+v", gateway.messages)
	}
}

func TestLarkConnectorHandleEventFetchesBotIdentity(t *testing.T) {
	connector := newTestEventConnector(t)
	gateway := &fakeSDKGateway{}
	store := newFakeSDKAccountStore()
	account := sdkAccount("account-1", "cli_1", "secret_1", "")
	delete(account.Credential, "bot_open_id")
	var sawBotInfo bool
	httpClient := &http.Client{Transport: testRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			return testJSONResponse(map[string]any{"code": 0, "msg": "ok", "tenant_access_token": "tenant-token", "expire": 7200})
		case "/open-apis/bot/v3/info":
			sawBotInfo = true
			if got := req.Header.Get("Authorization"); got != "Bearer tenant-token" {
				t.Fatalf("auth=%q", got)
			}
			return testJSONResponse(map[string]any{"code": 0, "msg": "ok", "bot": map[string]any{"open_id": "ou_bot_live", "app_name": "Beak Bot"}})
		case "/open-apis/contact/v3/users/ou_user", "/open-apis/im/v1/chats/oc_group":
			return testJSONResponse(map[string]any{"code": 99991663, "msg": "permission denied"})
		default:
			t.Fatalf("unexpected request: %s", req.URL.Path)
			return nil, nil
		}
	})}
	body := []byte(`{
		"app_id":"cli_1",
		"event_id":"evt_fetch_bot",
		"event_type":"im.message.receive_v1",
		"sender":{"sender_id":{"open_id":"ou_user"},"sender_type":"user"},
		"message":{"message_id":"om_fetch_bot","chat_id":"oc_group","chat_type":"group","message_type":"text","content":"{\"text\":\"@_bot ping\"}","create_time":"1770000000000","mentions":[{"key":"@_bot","id":{"open_id":"ou_bot_live"},"name":"Beak Bot"}]}
	}`)
	result, err := connector.HandleEvent(context.Background(), sdk.Runtime{
		WorkspaceUUID: "workspace-1",
		Channel:       sdk.Channel{UUID: "channel-1", WorkspaceUUID: "workspace-1", Platform: Platform},
		Account:       account,
		Gateway:       gateway,
		AccountStore:  store,
		HTTPClient:    httpClient,
	}, account, body)
	if err != nil {
		t.Fatal(err)
	}
	if !sawBotInfo {
		t.Fatal("bot info endpoint was not called")
	}
	if result.Inbound == nil || !result.Inbound.MentionedMe || result.Inbound.Text != "ping" {
		t.Fatalf("inbound=%+v", result.Inbound)
	}
	if got := store.state("account-1")["bot_open_id"]; got != "ou_bot_live" {
		t.Fatalf("state bot_open_id=%#v", got)
	}
	if got := store.state("account-1")["bot_name"]; got != "Beak Bot" {
		t.Fatalf("state bot_name=%#v", got)
	}
	identity, ok := store.state("account-1")["bot_identity"].(map[string]any)
	if !ok || identity["id"] != "ou_bot_live" || identity["id_type"] != "open_id" || identity["display_name"] != "Beak Bot" {
		t.Fatalf("bot_identity=%+v state=%+v", store.state("account-1")["bot_identity"], store.state("account-1"))
	}
}

func TestLarkConnectorHandleEventMatchesBotNameForUserIDPostMention(t *testing.T) {
	connector := newTestEventConnector(t)
	gateway := &fakeSDKGateway{}
	store := newFakeSDKAccountStore()
	account := sdkAccount("account-1", "cli_1", "secret_1", "")
	var sawBotInfo bool
	httpClient := &http.Client{Transport: testRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			return testJSONResponse(map[string]any{"code": 0, "msg": "ok", "tenant_access_token": "tenant-token", "expire": 7200})
		case "/open-apis/bot/v3/info":
			sawBotInfo = true
			return testJSONResponse(map[string]any{"code": 0, "msg": "ok", "bot": map[string]any{"open_id": "ou_bot", "app_name": "Beak Bot"}})
		case "/open-apis/contact/v3/users/ou_user", "/open-apis/im/v1/chats/oc_group":
			return testJSONResponse(map[string]any{"code": 99991663, "msg": "permission denied"})
		default:
			t.Fatalf("unexpected request: %s", req.URL.Path)
			return nil, nil
		}
	})}
	body := []byte(`{
		"app_id":"cli_1",
		"event_id":"evt_user_id_bot_mention",
		"event_type":"im.message.receive_v1",
		"sender":{"sender_id":{"open_id":"ou_user"},"sender_type":"user"},
		"message":{"message_id":"om_user_id_bot_mention","chat_id":"oc_group","chat_type":"group","message_type":"post","content":"{\"zh_cn\":{\"content\":[[{\"tag\":\"at\",\"user_id\":\"user_bot\",\"user_name\":\"Beak Bot\"},{\"tag\":\"text\",\"text\":\" ping\"}]]}}","create_time":"1770000000000","mentions":[{"id":{"user_id":"user_bot"},"name":"Beak Bot"}]}
	}`)
	result, err := connector.HandleEvent(context.Background(), sdk.Runtime{
		WorkspaceUUID: "workspace-1",
		Channel:       sdk.Channel{UUID: "channel-1", WorkspaceUUID: "workspace-1", Platform: Platform},
		Account:       account,
		Gateway:       gateway,
		AccountStore:  store,
		HTTPClient:    httpClient,
	}, account, body)
	if err != nil {
		t.Fatal(err)
	}
	if !sawBotInfo {
		t.Fatal("bot info endpoint was not called")
	}
	if result.Inbound == nil || !result.Inbound.MentionedMe || result.Inbound.Text != "ping" {
		t.Fatalf("inbound=%+v", result.Inbound)
	}
	if len(gateway.messages) != 1 || gateway.messages[0].Content != "ping" {
		t.Fatalf("messages=%+v", gateway.messages)
	}
	if got := store.state("account-1")["bot_name"]; got != "Beak Bot" {
		t.Fatalf("state bot_name=%#v", got)
	}
}

func TestLarkConnectorHandleEventRejectsMismatchedAppID(t *testing.T) {
	connector := newTestEventConnector(t)
	gateway := &fakeSDKGateway{}
	account := sdkAccount("account-1", "cli_1", "secret_1", "")
	body := []byte(`{
		"app_id":"cli_other",
		"event_id":"evt_1",
		"event_type":"im.message.receive_v1",
		"sender":{"sender_id":{"open_id":"ou_user"},"sender_type":"user"},
		"message":{"message_id":"om_1","chat_id":"oc_group","chat_type":"group","message_type":"text","content":"{\"text\":\"wrong app\"}","create_time":"1770000000000"}
	}`)
	result, err := connector.HandleEvent(context.Background(), sdk.Runtime{
		WorkspaceUUID: "workspace-1",
		Channel:       sdk.Channel{UUID: "channel-1", WorkspaceUUID: "workspace-1", Platform: Platform},
		Account:       account,
		Gateway:       gateway,
		AccountStore:  newFakeSDKAccountStore(),
	}, account, body)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !result.Ignored || result.Reason != "app_id_mismatch" {
		t.Fatalf("result=%+v", result)
	}
	if len(gateway.messages) != 0 {
		t.Fatalf("messages=%+v", gateway.messages)
	}
}

func TestLarkConnectorWebhookMentionAll(t *testing.T) {
	connector := newTestWebhookConnector(t)
	gateway := &fakeSDKGateway{}
	store := newFakeSDKAccountStore()
	account := sdkAccount("account-1", "cli_1", "secret_1", "")
	body := []byte(`{
		"schema":"2.0",
		"header":{"event_id":"evt_all","event_type":"im.message.receive_v1","app_id":"cli_1","token":"verify-token"},
		"event":{
			"sender":{"sender_id":{"open_id":"ou_user"},"sender_type":"user"},
			"message":{"message_id":"om_all","chat_id":"oc_group","chat_type":"group","message_type":"text","content":"{\"text\":\"hello all\"}","create_time":"1770000000000","mentions":[{"key":"@_all","id":{},"name":"所有人"}]}
		}
	}`)
	result, err := connector.HandleWebhook(context.Background(), sdk.Runtime{
		WorkspaceUUID: "workspace-1",
		Channel:       sdk.Channel{UUID: "channel-1", WorkspaceUUID: "workspace-1", Platform: Platform},
		Account:       account,
		Gateway:       gateway,
		AccountStore:  store,
	}, account, body)
	if err != nil {
		t.Fatal(err)
	}
	if result.Inbound == nil || !result.Inbound.MentionAll || result.Inbound.MentionedMe || len(result.Inbound.Mentions) != 1 {
		t.Fatalf("inbound=%+v", result.Inbound)
	}
	if result.Inbound.Mentions[0].ID != "all" || result.Inbound.Mentions[0].IDType != "mention_all" {
		t.Fatalf("mentions=%+v", result.Inbound.Mentions)
	}
}

func TestLarkConnectorWebhookOnlyBotMentionIsDelivered(t *testing.T) {
	connector := newTestWebhookConnector(t)
	gateway := &fakeSDKGateway{}
	store := newFakeSDKAccountStore()
	account := sdkAccount("account-1", "cli_1", "secret_1", "")
	body := []byte(`{
		"schema":"2.0",
		"header":{"event_id":"evt_only_bot","event_type":"im.message.receive_v1","app_id":"cli_1","token":"verify-token"},
		"event":{
			"sender":{"sender_id":{"open_id":"ou_user"},"sender_type":"user"},
			"message":{"message_id":"om_only_bot","chat_id":"oc_group","chat_type":"group","message_type":"text","content":"{\"text\":\"@_bot\"}","create_time":"1770000000000","mentions":[{"key":"@_bot","id":{"open_id":"ou_bot"},"name":"Beak Bot"}]}
		}
	}`)
	result, err := connector.HandleWebhook(context.Background(), sdk.Runtime{
		WorkspaceUUID: "workspace-1",
		Channel:       sdk.Channel{UUID: "channel-1", WorkspaceUUID: "workspace-1", Platform: Platform},
		Account:       account,
		Gateway:       gateway,
		AccountStore:  store,
	}, account, body)
	if err != nil {
		t.Fatal(err)
	}
	if result.Ignored || result.Inbound == nil || !result.Inbound.MentionedMe || strings.TrimSpace(result.Inbound.Text) != "" {
		t.Fatalf("result=%+v", result)
	}
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	if len(gateway.messages) != 1 || strings.TrimSpace(gateway.messages[0].Content) != "" {
		t.Fatalf("messages=%+v", gateway.messages)
	}
}

func TestLarkConnectorWebhookThreadIDPropagates(t *testing.T) {
	connector := newTestWebhookConnector(t)
	gateway := &fakeSDKGateway{}
	store := newFakeSDKAccountStore()
	account := sdkAccount("account-1", "cli_1", "secret_1", "")
	body := []byte(`{
		"schema":"2.0",
		"header":{"event_id":"evt_thread","event_type":"im.message.receive_v1","app_id":"cli_1","token":"verify-token"},
		"event":{
			"sender":{"sender_id":{"open_id":"ou_user"},"sender_type":"user"},
			"message":{"message_id":"om_thread_reply","root_id":"om_root","parent_id":"om_parent","thread_id":"omt_thread","chat_id":"oc_group","chat_type":"group","message_type":"text","content":"{\"text\":\"thread ping\"}","create_time":"1770000000000"}
		}
	}`)
	result, err := connector.HandleWebhook(context.Background(), sdk.Runtime{
		WorkspaceUUID: "workspace-1",
		Channel:       sdk.Channel{UUID: "channel-1", WorkspaceUUID: "workspace-1", Platform: Platform},
		Account:       account,
		Gateway:       gateway,
		AccountStore:  store,
	}, account, body)
	if err != nil {
		t.Fatal(err)
	}
	if result.Inbound == nil || result.Inbound.ThreadID != "omt_thread" {
		t.Fatalf("inbound=%+v", result.Inbound)
	}
	if result.Inbound.ChatIdentity.ID != "oc_group" || result.Inbound.ChatIdentity.Type != sdk.ChatTypeGroup {
		t.Fatalf("chat identity=%+v", result.Inbound.ChatIdentity)
	}
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	if len(gateway.chatSessions) != 1 || gateway.chatSessions[0].ThreadID != "omt_thread" {
		t.Fatalf("chatSessions=%+v", gateway.chatSessions)
	}
	if gateway.chatSessions[0].ChatIdentity.ID != "oc_group" || gateway.chatSessions[0].ChatIdentity.Type != sdk.ChatTypeGroup {
		t.Fatalf("chat session identity=%+v", gateway.chatSessions[0].ChatIdentity)
	}
	inbound, ok := gateway.messages[0].Metadata["inbound_message"].(sdk.InboundMessage)
	if !ok || inbound.ThreadID != "omt_thread" {
		t.Fatalf("metadata inbound=%+v metadata=%+v", inbound, gateway.messages[0].Metadata)
	}
	if inbound.ChatIdentity.ID != "oc_group" || inbound.ChatIdentity.Type != sdk.ChatTypeGroup {
		t.Fatalf("metadata inbound identity=%+v", inbound.ChatIdentity)
	}
	state := store.state("account-1")
	peerSessions, ok := state["peer_sessions"].(map[string]string)
	if !ok || peerSessions["group:oc_group"] != result.SessionUUID {
		t.Fatalf("peer sessions=%+v", state["peer_sessions"])
	}
	if _, ok := peerSessions["group:oc_group:thread:omt_thread"]; ok {
		t.Fatalf("peer sessions should stay chat-scoped, got %+v", peerSessions)
	}
}

func TestLarkConnectorWebhookResolvesDisplayNames(t *testing.T) {
	connector := newTestWebhookConnector(t)
	gateway := &fakeSDKGateway{}
	store := newFakeSDKAccountStore()
	account := sdkAccount("account-1", "cli_1", "secret_1", "")
	var sawUserInfo bool
	var sawChatInfo bool
	httpClient := &http.Client{Transport: testRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			return testJSONResponse(map[string]any{"code": 0, "msg": "ok", "tenant_access_token": "tenant-token", "expire": 7200})
		case "/open-apis/contact/v3/users/ou_user":
			sawUserInfo = true
			if got := req.URL.Query().Get("user_id_type"); got != "open_id" {
				t.Fatalf("user_id_type=%q", got)
			}
			if got := req.Header.Get("Authorization"); got != "Bearer tenant-token" {
				t.Fatalf("auth=%q", got)
			}
			return testJSONResponse(map[string]any{
				"code": 0,
				"msg":  "ok",
				"data": map[string]any{"user": map[string]any{"open_id": "ou_user", "name": "Alice"}},
			})
		case "/open-apis/im/v1/chats/oc_group":
			sawChatInfo = true
			if got := req.Header.Get("Authorization"); got != "Bearer tenant-token" {
				t.Fatalf("auth=%q", got)
			}
			return testJSONResponse(map[string]any{
				"code": 0,
				"msg":  "ok",
				"data": map[string]any{"chat_id": "oc_group", "name": "Team", "avatar_url": "https://example.test/team.png"},
			})
		default:
			t.Fatalf("unexpected request: %s", req.URL.Path)
		}
		return nil, nil
	})}
	body := []byte(`{
		"schema":"2.0",
		"header":{"event_id":"evt_display_name","event_type":"im.message.receive_v1","app_id":"cli_1","token":"verify-token"},
		"event":{
			"sender":{"sender_id":{"open_id":"ou_user"},"sender_type":"user"},
			"message":{"message_id":"om_display_name","chat_id":"oc_group","chat_type":"group","message_type":"text","content":"{\"text\":\"hello names\"}","create_time":"1770000000000"}
		}
	}`)
	result, err := connector.HandleWebhook(context.Background(), sdk.Runtime{
		WorkspaceUUID: "workspace-1",
		Channel:       sdk.Channel{UUID: "channel-1", WorkspaceUUID: "workspace-1", Platform: Platform},
		Account:       account,
		Gateway:       gateway,
		AccountStore:  store,
		HTTPClient:    httpClient,
	}, account, body)
	if err != nil {
		t.Fatal(err)
	}
	if !sawUserInfo || !sawChatInfo {
		t.Fatalf("sawUserInfo=%v sawChatInfo=%v", sawUserInfo, sawChatInfo)
	}
	if result.Inbound == nil || result.Inbound.SenderDisplayName != "Alice" || result.Inbound.ChatDisplayName != "Team" || result.Inbound.ChatAvatarURL != "https://example.test/team.png" {
		t.Fatalf("inbound=%+v", result.Inbound)
	}
	if result.Inbound.ChatIdentity.DisplayName != "Team" {
		t.Fatalf("chat identity=%+v", result.Inbound.ChatIdentity)
	}
	if result.Inbound.ChatIdentity.AvatarURL != "https://example.test/team.png" {
		t.Fatalf("chat identity avatar=%+v", result.Inbound.ChatIdentity)
	}
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	if len(gateway.chatSessions) != 1 {
		t.Fatalf("chatSessions=%+v", gateway.chatSessions)
	}
	chatReq := gateway.chatSessions[0]
	if chatReq.ChatDisplayName != "Team" || chatReq.ChatAvatarURL != "https://example.test/team.png" || chatReq.ChatIdentity.DisplayName != "Team" {
		t.Fatalf("chatReq=%+v", chatReq)
	}
	if chatReq.ChatIdentity.AvatarURL != "https://example.test/team.png" || chatReq.Metadata["chat_display_name"] != "Team" || chatReq.Metadata["chat_avatar_url"] != "https://example.test/team.png" {
		t.Fatalf("chatReq identity/metadata=%+v metadata=%+v", chatReq.ChatIdentity, chatReq.Metadata)
	}
	inbound, ok := gateway.messages[0].Metadata["inbound_message"].(sdk.InboundMessage)
	if !ok || inbound.SenderDisplayName != "Alice" || inbound.ChatDisplayName != "Team" || inbound.ChatIdentity.DisplayName != "Team" {
		t.Fatalf("metadata inbound=%+v metadata=%+v", inbound, gateway.messages[0].Metadata)
	}
	if gateway.messages[0].Metadata["chat_display_name"] != "Team" || gateway.messages[0].Metadata["chat_avatar_url"] != "https://example.test/team.png" || gateway.messages[0].Metadata["sender_display_name"] != "Alice" {
		t.Fatalf("message metadata=%+v", gateway.messages[0].Metadata)
	}
	state := store.state("account-1")
	if users, ok := state["user_display_names"].(map[string]string); !ok || users["ou_user"] != "Alice" {
		t.Fatalf("user_display_names=%+v", state["user_display_names"])
	}
	if chats, ok := state["chat_display_names"].(map[string]string); !ok || chats["oc_group"] != "Team" {
		t.Fatalf("chat_display_names=%+v", state["chat_display_names"])
	}
}

func TestLarkConnectorWebhookIgnoresDisplayNameLookupFailure(t *testing.T) {
	connector := newTestWebhookConnector(t)
	gateway := &fakeSDKGateway{}
	store := newFakeSDKAccountStore()
	account := sdkAccount("account-1", "cli_1", "secret_1", "")
	httpClient := &http.Client{Transport: testRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			return testJSONResponse(map[string]any{"code": 0, "msg": "ok", "tenant_access_token": "tenant-token", "expire": 7200})
		case "/open-apis/contact/v3/users/ou_user", "/open-apis/im/v1/chats/oc_group":
			return testJSONResponse(map[string]any{"code": 99991663, "msg": "permission denied"})
		default:
			t.Fatalf("unexpected request: %s", req.URL.Path)
		}
		return nil, nil
	})}
	body := []byte(`{
		"schema":"2.0",
		"header":{"event_id":"evt_display_name_failed","event_type":"im.message.receive_v1","app_id":"cli_1","token":"verify-token"},
		"event":{
			"sender":{"sender_id":{"open_id":"ou_user"},"sender_type":"user"},
			"message":{"message_id":"om_display_name_failed","chat_id":"oc_group","chat_type":"group","message_type":"text","content":"{\"text\":\"hello without names\"}","create_time":"1770000000000"}
		}
	}`)
	result, err := connector.HandleWebhook(context.Background(), sdk.Runtime{
		WorkspaceUUID: "workspace-1",
		Channel:       sdk.Channel{UUID: "channel-1", WorkspaceUUID: "workspace-1", Platform: Platform},
		Account:       account,
		Gateway:       gateway,
		AccountStore:  store,
		HTTPClient:    httpClient,
	}, account, body)
	if err != nil {
		t.Fatal(err)
	}
	if result.Ignored || result.Inbound == nil || result.Inbound.Text != "hello without names" {
		t.Fatalf("result=%+v", result)
	}
	if result.Inbound.SenderDisplayName != "" || result.Inbound.ChatDisplayName != "" {
		t.Fatalf("inbound=%+v", result.Inbound)
	}
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	if len(gateway.messages) != 1 || gateway.messages[0].Content != "hello without names" {
		t.Fatalf("messages=%+v", gateway.messages)
	}
}

func TestLarkConnectorSendUsesRequestedAccount(t *testing.T) {
	httpClient := &http.Client{Transport: testRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["app_id"] != "cli_2" || body["app_secret"] != "secret_2" {
				t.Fatalf("token body=%+v", body)
			}
			return testJSONResponse(map[string]any{"code": 0, "msg": "ok", "tenant_access_token": "token-2", "expire": 7200})
		case "/open-apis/im/v1/messages":
			if got := r.Header.Get("Authorization"); got != "Bearer token-2" {
				t.Fatalf("auth=%q", got)
			}
			if got := r.URL.Query().Get("receive_id_type"); got != "chat_id" {
				t.Fatalf("receive_id_type=%q", got)
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
			if body.ReceiveID != "oc_group" || body.MsgType != "text" || body.UUID != "message-uuid" {
				t.Fatalf("send body=%+v", body)
			}
			var content map[string]string
			if err := json.Unmarshal([]byte(body.Content), &content); err != nil {
				t.Fatal(err)
			}
			if content["text"] != "reply" {
				t.Fatalf("content=%+v", content)
			}
			return testJSONResponse(map[string]any{"code": 0, "msg": "ok", "data": map[string]any{"message_id": "om_reply", "chat_id": "oc_group"}})
		default:
			t.Fatalf("unexpected request: %s", r.URL.Path)
		}
		return nil, nil
	})}

	connector := NewConnector()
	result, err := connector.Send(context.Background(), sdk.Runtime{
		HTTPClient: httpClient,
		Accounts: []sdk.ChannelAccount{
			sdkAccount("account-1", "cli_1", "secret_1", "https://open.feishu.test"),
			sdkAccount("account-2", "cli_2", "secret_2", "https://open.feishu.test"),
		},
	}, sdk.OutboundMessage{
		AccountUUID: "account-2",
		ChatType:    sdk.ChatTypeGroup,
		ChatID:      "oc_group",
		Text:        "reply",
		MessageUUID: "message-uuid",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.AccountUUID != "account-2" || result.Platform != Platform || result.MessageID != "om_reply" {
		t.Fatalf("result=%+v", result)
	}
}

func TestLarkConnectorSendMarkdownFormat(t *testing.T) {
	httpClient := &http.Client{Transport: testRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			return testJSONResponse(map[string]any{"code": 0, "msg": "ok", "tenant_access_token": "token-1", "expire": 7200})
		case "/open-apis/im/v1/messages":
			var body struct {
				ReceiveID string `json:"receive_id"`
				MsgType   string `json:"msg_type"`
				Content   string `json:"content"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.ReceiveID != "oc_group" || body.MsgType != "post" {
				t.Fatalf("send body=%+v", body)
			}
			var content struct {
				ZhCN struct {
					Title   string `json:"title"`
					Content [][]struct {
						Tag  string `json:"tag"`
						Text string `json:"text"`
					} `json:"content"`
				} `json:"zh_cn"`
			}
			if err := json.Unmarshal([]byte(body.Content), &content); err != nil {
				t.Fatal(err)
			}
			if content.ZhCN.Title != "日志查询" || len(content.ZhCN.Content) != 1 || len(content.ZhCN.Content[0]) != 1 {
				t.Fatalf("content=%+v", content)
			}
			item := content.ZhCN.Content[0][0]
			if item.Tag != "md" || item.Text != "# 日志查询\n- 错误日志" {
				t.Fatalf("markdown item=%+v", item)
			}
			return testJSONResponse(map[string]any{"code": 0, "msg": "ok", "data": map[string]any{"message_id": "om_markdown", "chat_id": "oc_group"}})
		default:
			t.Fatalf("unexpected request: %s", r.URL.Path)
		}
		return nil, nil
	})}

	result, err := NewConnector().Send(context.Background(), sdk.Runtime{
		HTTPClient: httpClient,
		Account:    sdkAccount("account-1", "cli_1", "secret_1", "https://open.feishu.test"),
	}, sdk.OutboundMessage{
		AccountUUID: "account-1",
		ChatType:    sdk.ChatTypeGroup,
		ChatID:      "oc_group",
		Text:        "# 日志查询\n- 错误日志",
		Format:      "markdown",
		Title:       "日志查询",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.MessageID != "om_markdown" || result.Raw["msg_type"] != "post" {
		t.Fatalf("result=%+v", result)
	}
}

func TestLarkOutboundMarkdownFormatAliases(t *testing.T) {
	req := sdk.OutboundMessage{Raw: map[string]any{"content_type": "text/markdown"}}
	if !isMarkdownOutbound(req) {
		t.Fatalf("isMarkdownOutbound() = false, want true for text/markdown")
	}
}

func TestLarkConnectorSendMarkdownTitleIgnoresMentionPrefix(t *testing.T) {
	httpClient := &http.Client{Transport: testRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			return testJSONResponse(map[string]any{"code": 0, "msg": "ok", "tenant_access_token": "token-1", "expire": 7200})
		case "/open-apis/im/v1/messages":
			var body struct {
				MsgType string `json:"msg_type"`
				Content string `json:"content"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.MsgType != "post" {
				t.Fatalf("send body=%+v", body)
			}
			var content struct {
				ZhCN struct {
					Title   string `json:"title"`
					Content [][]struct {
						Text string `json:"text"`
					} `json:"content"`
				} `json:"zh_cn"`
			}
			if err := json.Unmarshal([]byte(body.Content), &content); err != nil {
				t.Fatal(err)
			}
			if content.ZhCN.Title != "日志查询" {
				t.Fatalf("title=%q", content.ZhCN.Title)
			}
			if got := content.ZhCN.Content[0][0].Text; !strings.HasPrefix(got, `<at user_id="all">Everyone</at>`) {
				t.Fatalf("markdown text=%q", got)
			}
			return testJSONResponse(map[string]any{"code": 0, "msg": "ok", "data": map[string]any{"message_id": "om_markdown", "chat_id": "oc_group"}})
		default:
			t.Fatalf("unexpected request: %s", r.URL.Path)
		}
		return nil, nil
	})}

	_, err := NewConnector().Send(context.Background(), sdk.Runtime{
		HTTPClient: httpClient,
		Account:    sdkAccount("account-1", "cli_1", "secret_1", "https://open.feishu.test"),
	}, sdk.OutboundMessage{
		AccountUUID: "account-1",
		ChatType:    sdk.ChatTypeGroup,
		ChatID:      "oc_group",
		Text:        "# 日志查询\n- 错误日志",
		Format:      "markdown",
		MentionAll:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestLarkConnectorSendPersistsTokenForEmptyState(t *testing.T) {
	var tokenCalls int
	httpClient := &http.Client{Transport: testRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			tokenCalls++
			return testJSONResponse(map[string]any{"code": 0, "msg": "ok", "tenant_access_token": "tenant-empty-state", "expire": 7200})
		case "/open-apis/im/v1/messages":
			if got := r.Header.Get("Authorization"); got != "Bearer tenant-empty-state" {
				t.Fatalf("auth=%q", got)
			}
			return testJSONResponse(map[string]any{"code": 0, "msg": "ok", "data": map[string]any{"message_id": "om_empty", "chat_id": "oc_group"}})
		default:
			t.Fatalf("unexpected request: %s", r.URL.Path)
		}
		return nil, nil
	})}
	store := newFakeSDKAccountStore()
	result, err := NewConnector().Send(context.Background(), sdk.Runtime{
		HTTPClient:   httpClient,
		Account:      sdkAccount("account-1", "cli_1", "secret_1", "https://open.feishu.test"),
		AccountStore: store,
	}, sdk.OutboundMessage{
		AccountUUID: "account-1",
		ChatType:    sdk.ChatTypeGroup,
		ChatID:      "oc_group",
		Text:        "reply",
	})
	if err != nil {
		t.Fatal(err)
	}
	if tokenCalls != 1 || result.MessageID != "om_empty" {
		t.Fatalf("tokenCalls=%d result=%+v", tokenCalls, result)
	}
	state := store.state("account-1")
	if state["tenant_access_token"] != "tenant-empty-state" || state["tenant_access_token_expires_at"] == nil {
		t.Fatalf("saved state=%+v", state)
	}
}

func TestLarkConnectorSendTextMentions(t *testing.T) {
	httpClient := &http.Client{Transport: testRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			return testJSONResponse(map[string]any{"code": 0, "msg": "ok", "tenant_access_token": "token-1", "expire": 7200})
		case "/open-apis/im/v1/messages":
			var body struct {
				Content string `json:"content"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			var content map[string]string
			if err := json.Unmarshal([]byte(body.Content), &content); err != nil {
				t.Fatal(err)
			}
			want := `<at user_id="all">Everyone</at> <at user_id="ou_user">Alice</at>` + "\nreply"
			if content["text"] != want {
				t.Fatalf("content=%q want=%q", content["text"], want)
			}
			return testJSONResponse(map[string]any{"code": 0, "msg": "ok", "data": map[string]any{"message_id": "om_reply", "chat_id": "oc_group"}})
		default:
			t.Fatalf("unexpected request: %s", r.URL.Path)
		}
		return nil, nil
	})}

	_, err := NewConnector().Send(context.Background(), sdk.Runtime{
		HTTPClient: httpClient,
		Account:    sdkAccount("account-1", "cli_1", "secret_1", "https://open.feishu.test"),
	}, sdk.OutboundMessage{
		AccountUUID: "account-1",
		ChatType:    sdk.ChatTypeGroup,
		ChatID:      "oc_group",
		Text:        "reply",
		MentionAll:  true,
		Mentions: []sdk.MentionIdentity{
			{ID: "ou_user", IDType: "open_id", DisplayName: "Alice"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestLarkConnectorSendTextRawMentions(t *testing.T) {
	httpClient := &http.Client{Transport: testRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			return testJSONResponse(map[string]any{"code": 0, "msg": "ok", "tenant_access_token": "token-1", "expire": 7200})
		case "/open-apis/im/v1/messages":
			var body struct {
				Content string `json:"content"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			var content map[string]string
			if err := json.Unmarshal([]byte(body.Content), &content); err != nil {
				t.Fatal(err)
			}
			want := `<at user_id="all">Everyone</at> <at user_id="ou_list">ou_list</at> <at user_id="ou_raw">Bob</at>` + "\nreply"
			if content["text"] != want {
				t.Fatalf("content=%q want=%q", content["text"], want)
			}
			return testJSONResponse(map[string]any{"code": 0, "msg": "ok", "data": map[string]any{"message_id": "om_reply", "chat_id": "oc_group"}})
		default:
			t.Fatalf("unexpected request: %s", r.URL.Path)
		}
		return nil, nil
	})}

	_, err := NewConnector().Send(context.Background(), sdk.Runtime{
		HTTPClient: httpClient,
		Account:    sdkAccount("account-1", "cli_1", "secret_1", "https://open.feishu.test"),
	}, sdk.OutboundMessage{
		AccountUUID: "account-1",
		ChatType:    sdk.ChatTypeGroup,
		ChatID:      "oc_group",
		Text:        "reply",
		Raw: map[string]any{
			"mentionAll":  true,
			"mention_ids": []any{"ou_list"},
			"mentions": []any{
				map[string]any{"id": "ou_raw", "display_name": "Bob"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestLarkConnectorSendReplyAndRawContent(t *testing.T) {
	httpClient := &http.Client{Transport: testRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			return testJSONResponse(map[string]any{"code": 0, "msg": "ok", "tenant_access_token": "token-1", "expire": 7200})
		case "/open-apis/im/v1/messages/om_parent/reply":
			var body struct {
				MsgType       string `json:"msg_type"`
				Content       string `json:"content"`
				UUID          string `json:"uuid"`
				ReplyInThread bool   `json:"reply_in_thread"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.MsgType != "post" || body.Content != `{"zh_cn":{"title":"hi"}}` || !body.ReplyInThread {
				t.Fatalf("body=%+v", body)
			}
			return testJSONResponse(map[string]any{"code": 0, "msg": "ok", "data": map[string]any{"message_id": "om_reply", "chat_id": "oc_group"}})
		default:
			t.Fatalf("unexpected request: %s", r.URL.Path)
		}
		return nil, nil
	})}

	result, err := NewConnector().Send(context.Background(), sdk.Runtime{
		HTTPClient: httpClient,
		Account:    sdkAccount("account-1", "cli_1", "secret_1", "https://open.feishu.test"),
	}, sdk.OutboundMessage{
		AccountUUID: "account-1",
		ChatType:    sdk.ChatTypeGroup,
		ChatID:      "oc_group",
		Text:        "ignored",
		MessageUUID: "uuid-1",
		Raw: map[string]any{
			"reply_to_message_id": "om_parent",
			"reply_in_thread":     true,
			"msg_type":            "post",
			"content":             map[string]any{"zh_cn": map[string]any{"title": "hi"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.MessageID != "om_reply" || result.Raw["msg_type"] != "post" {
		t.Fatalf("result=%+v", result)
	}
}

func sdkAccount(uuid, appID, appSecret, baseURL string) sdk.ChannelAccount {
	credential := map[string]any{
		"account_id":         uuid,
		"app_id":             appID,
		"app_secret":         appSecret,
		"verification_token": "verify-token",
		"brand":              "feishu",
		"bot_open_id":        "ou_bot",
	}
	if baseURL != "" {
		credential["base_url"] = baseURL
	}
	return sdk.ChannelAccount{
		UUID:          uuid,
		WorkspaceUUID: "workspace-1",
		ChannelUUID:   "channel-1",
		Platform:      Platform,
		Credential:    credential,
		State:         map[string]any{},
	}
}

type fakeSDKGateway struct {
	mu                     sync.Mutex
	channelPlatform        string
	channelLinkAccountUUID string
	chatSessions           []sdk.EnsureChatSessionRequest
	messages               []sdk.CreateMessageRequest
}

func (g *fakeSDKGateway) EnsureChannel(ctx context.Context, req sdk.EnsureChannelRequest) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.channelPlatform = req.Platform
	return "channel-1", nil
}

func (g *fakeSDKGateway) EnsureChannelLinkSession(ctx context.Context, req sdk.EnsureChannelLinkSessionRequest) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.channelLinkAccountUUID = req.AccountUUID
	return "link-" + req.AccountUUID, nil
}

func (g *fakeSDKGateway) EnsureChatSession(ctx context.Context, req sdk.EnsureChatSessionRequest) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.chatSessions = append(g.chatSessions, req)
	return "session-1", nil
}

func (g *fakeSDKGateway) CreateMessage(ctx context.Context, req sdk.CreateMessageRequest) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.messages = append(g.messages, req)
	return "message-1", nil
}

func (g *fakeSDKGateway) StreamSession(ctx context.Context, req sdk.StreamSessionRequest, handle func(sdk.StreamEvent) error) error {
	return nil
}

func (g *fakeSDKGateway) AgentParticipantID() string {
	return "agent:agent-1"
}

func (g *fakeSDKGateway) BridgeParticipantID(platform string) string {
	return sdk.BridgeParticipantID(platform)
}

type fakeSDKAccountStore struct {
	mu     sync.Mutex
	states map[string]map[string]any
}

func newFakeSDKAccountStore() *fakeSDKAccountStore {
	return &fakeSDKAccountStore{states: make(map[string]map[string]any)}
}

func (s *fakeSDKAccountStore) SaveChannelAccountState(ctx context.Context, accountUUID string, state map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.states[accountUUID] = state
	return nil
}

func (s *fakeSDKAccountStore) LoadChannelAccountState(ctx context.Context, accountUUID string) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.states[accountUUID], nil
}

func (s *fakeSDKAccountStore) state(accountUUID string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.states[accountUUID]
}

type testRoundTripFunc func(*http.Request) (*http.Response, error)

func (f testRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func testJSONResponse(body any) (*http.Response, error) {
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

func encryptWebhookForTest(t *testing.T, plain string, key string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(key))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		t.Fatal(err)
	}
	padding := aes.BlockSize - len(plain)%aes.BlockSize
	padded := append([]byte(plain), bytes.Repeat([]byte{byte(padding)}, padding)...)
	iv := []byte("1234567890abcdef")
	out := make([]byte, len(iv)+len(padded))
	copy(out, iv)
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out[len(iv):], padded)
	return base64.StdEncoding.EncodeToString(out)
}

func signLarkWebhookRequest(req *http.Request, body []byte, encryptKey string) {
	signLarkWebhookRequestAt(req, body, encryptKey, time.Now().UTC())
}

func signLarkWebhookRequestAt(req *http.Request, body []byte, encryptKey string, now time.Time) {
	timestamp := fmt.Sprintf("%d", now.Unix())
	nonce := "nonce-1"
	req.Header.Set("X-Lark-Request-Timestamp", timestamp)
	req.Header.Set("X-Lark-Request-Nonce", nonce)
	signature := sha256.Sum256([]byte(timestamp + nonce + encryptKey + string(body)))
	req.Header.Set("X-Lark-Signature", fmt.Sprintf("%x", signature[:]))
}
