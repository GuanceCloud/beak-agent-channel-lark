package beaklark

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
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
	connector := NewConnector()
	var _ sdk.Connector = connector

	metadata := connector.Metadata()
	if metadata.ID != ID || metadata.Platform != Platform || metadata.Label != "Lark/Feishu" {
		t.Fatalf("metadata=%+v", metadata)
	}
	if !metadata.Capabilities.Text || !metadata.Capabilities.DirectChat || !metadata.Capabilities.GroupChat || metadata.Capabilities.Media {
		t.Fatalf("capabilities=%+v", metadata.Capabilities)
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
}

func TestLarkConnectorStartEnsuresChannelLink(t *testing.T) {
	connector := NewConnector()
	gateway := &fakeSDKGateway{}
	store := newFakeSDKAccountStore()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	err := connector.Start(ctx, sdk.Runtime{
		WorkspaceUUID: "workspace-1",
		Channel:       sdk.Channel{UUID: "channel-1", WorkspaceUUID: "workspace-1", Platform: Platform},
		Account:       sdkAccount("account-1", "cli_1", "secret_1", ""),
		Gateway:       gateway,
		AccountStore:  store,
	})
	if !errors.Is(err, context.DeadlineExceeded) {
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
	connector := NewConnector()
	result, err := connector.HandleWebhook(context.Background(), sdk.Runtime{}, sdkAccount("account-1", "cli_1", "secret_1", ""), []byte(`{"type":"url_verification","challenge":"challenge-1","token":"verify-token"}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.Challenge != "challenge-1" || result.Type != "url_verification" {
		t.Fatalf("result=%+v", result)
	}
}

func TestLarkConnectorEncryptedWebhookChallenge(t *testing.T) {
	connector := NewConnector()
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
	connector := NewConnector()
	account := sdkAccount("account-1", "cli_1", "secret_1", "")
	account.Credential["encrypt_key"] = "encrypt-key"
	body := []byte(`{"type":"url_verification","challenge":"challenge-1","token":"verify-token"}`)
	req, err := http.NewRequest(http.MethodPost, "https://beak.test/webhook", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Lark-Request-Timestamp", "1770000000")
	req.Header.Set("X-Lark-Request-Nonce", "nonce-1")
	signature := sha256.Sum256([]byte("1770000000" + "nonce-1" + "encrypt-key" + string(body)))
	req.Header.Set("X-Lark-Signature", fmt.Sprintf("%x", signature[:]))
	result, err := connector.HandleWebhookRequest(context.Background(), sdk.Runtime{}, account, req)
	if err != nil {
		t.Fatal(err)
	}
	if result.Challenge != "challenge-1" {
		t.Fatalf("result=%+v", result)
	}
}

func TestLarkConnectorWebhookCreatesMessageAndDedupes(t *testing.T) {
	connector := NewConnector()
	gateway := &fakeSDKGateway{}
	store := newFakeSDKAccountStore()
	account := sdkAccount("account-1", "cli_1", "secret_1", "")
	body := []byte(`{
		"schema":"2.0",
		"header":{"event_id":"evt_1","event_type":"im.message.receive_v1","app_id":"cli_1","token":"verify-token"},
		"event":{
			"sender":{"sender_id":{"open_id":"ou_user"},"sender_type":"user"},
			"message":{"message_id":"om_1","chat_id":"oc_group","chat_type":"group","message_type":"text","content":"{\"text\":\"hello group\"}","create_time":"1770000000000"}
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
