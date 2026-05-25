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
	"time"

	"beak-agent-lark/sdk"
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	err := connector.Start(ctx, runtime)
	if !errors.Is(err, context.DeadlineExceeded) {
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
