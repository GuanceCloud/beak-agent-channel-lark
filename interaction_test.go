package beaklark

import (
	"context"
	"encoding/json"
	"github.com/GuanceCloud/beak-agent-channel-lark/sdk"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	"net/http"
	"strings"
	"testing"
)

type interactionTestGateway struct {
	*fakeSDKGateway
	action sdk.InteractionAction
}

type requesterDeniedGateway struct{ *fakeSDKGateway }

type requesterDenied struct{}

func (requesterDenied) Error() string                { return "denied" }
func (requesterDenied) InteractionErrorCode() string { return "actor_not_requester" }
func (g *requesterDeniedGateway) HandleInteraction(context.Context, sdk.InteractionAction) (*sdk.InteractionResult, error) {
	return nil, requesterDenied{}
}

func TestOtherMemberGetsPrivateWarningWithoutCardUpdate(t *testing.T) {
	account := sdkAccount("acct-1", "cli_1", "secret_1", "")
	runtime := sdk.Runtime{Account: account, Gateway: &requesterDeniedGateway{fakeSDKGateway: &fakeSDKGateway{}}}
	var event callback.CardActionTriggerEvent
	body := `{"schema":"2.0","header":{"event_id":"event-card","event_type":"card.action.trigger","app_id":"cli_1"},"event":{"operator":{"open_id":"ou_other"},"context":{"open_chat_id":"oc_chat","open_message_id":"om_card"},"action":{"value":{"beak_action":"approval_123:3:approve"}}}}`
	if err := json.Unmarshal([]byte(body), &event); err != nil {
		t.Fatal(err)
	}
	response, err := processLarkInteraction(context.Background(), runtime, account, &event)
	if err != nil || response == nil || response.Toast == nil || response.Toast.Type != "warning" || !strings.Contains(response.Toast.Content, "发起人") {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	account.Credential["verification_token"] = "verify-token"
	body = strings.Replace(body, `"app_id":"cli_1"`, `"app_id":"cli_1","token":"verify-token"`, 1)
	hook, err := (Connector{}).HandleWebhook(context.Background(), runtime, account, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	web, err := larkWebhookResponse(hook)
	if err != nil || !strings.Contains(string(web.Body), "发起人") || strings.Contains(string(web.Body), "已处理") {
		t.Fatalf("webhook response=%#v err=%v", web, err)
	}
}

func (g *interactionTestGateway) HandleInteraction(_ context.Context, a sdk.InteractionAction) (*sdk.InteractionResult, error) {
	g.action = a
	return &sdk.InteractionResult{State: "approved"}, nil
}
func TestLarkAndFeishuInteraction(t *testing.T) {
	for _, brand := range []string{"feishu", "lark"} {
		t.Run(brand, func(t *testing.T) {
			account := sdkAccount("acct-1", "cli_1", "secret_1", "")
			account.Credential["brand"] = brand
			account.Platform = brand
			gateway := &interactionTestGateway{fakeSDKGateway: &fakeSDKGateway{}}
			var payload map[string]any
			runtime := sdk.Runtime{Account: account, Gateway: gateway, AccountStore: newFakeSDKAccountStore(), HTTPClient: &http.Client{Transport: testRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				if strings.Contains(req.URL.Path, "tenant_access_token") {
					return testJSONResponse(map[string]any{"code": 0, "tenant_access_token": "test-token", "expire": 7200})
				}
				if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				return testJSONResponse(map[string]any{"code": 0, "data": map[string]string{"message_id": "om_card"}})
			})}}
			card := &sdk.InteractionCard{RequestID: "approval_123", Revision: 3, Kind: "tool_approval", State: "pending", Title: "Approval", Summary: "Run operation", Actions: []sdk.InteractionButton{{ID: "approve", Label: "Approve"}}}
			result, err := NewConnector().Send(context.Background(), runtime, sdk.OutboundMessage{AccountUUID: account.UUID, ChatID: "oc_chat", MessageUUID: "im_approval_12345678-1234-1234-1234-123456789abc_1_pending", Interaction: card})
			if err != nil || result.MessageID != "om_card" {
				t.Fatalf("send: %#v %v", result, err)
			}
			if id, _ := payload["uuid"].(string); id == "" || len(id) > 50 {
				t.Fatalf("invalid platform UUID: %q", id)
			}
			if payload["msg_type"] != "interactive" {
				t.Fatalf("not native card: %#v", payload)
			}
			body := []byte(`{"schema":"2.0","header":{"event_id":"event-card","event_type":"card.action.trigger","app_id":"cli_1","token":"verify-token"},"event":{"operator":{"open_id":"ou_actor"},"context":{"open_chat_id":"oc_chat","open_message_id":"om_card"},"action":{"value":{"beak_action":"approval_123:3:approve"}}}}`)
			if _, err := (Connector{}).HandleEvent(context.Background(), runtime, account, body); err != nil {
				t.Fatal(err)
			}
			if gateway.action.ActorID != "ou_actor" || gateway.action.ChatID != "oc_chat" {
				t.Fatalf("identity: %#v", gateway.action)
			}
		})
	}
}

// The host's integration-mock does not exercise the connector HTTP boundary.
// Cover dynamic detail, safe rendering and updates of the original message.
func TestToolApprovalCardLifecycle(t *testing.T) {
	for _, tool := range []string{"bash", "write_file", "mcp__database__execute", "<at id=all>```unsafe"} {
		t.Run(tool, func(t *testing.T) {
			account := sdkAccount("acct-1", "cli_1", "secret_1", "")
			var payload map[string]any
			var method, path string
			runtime := sdk.Runtime{Account: account, Gateway: &fakeSDKGateway{}, AccountStore: newFakeSDKAccountStore(), HTTPClient: &http.Client{Transport: testRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				if strings.Contains(req.URL.Path, "tenant_access_token") {
					return testJSONResponse(map[string]any{"code": 0, "tenant_access_token": "test-token", "expire": 7200})
				}
				method, path = req.Method, req.URL.Path
				if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				return testJSONResponse(map[string]any{"code": 0, "data": map[string]string{"message_id": "om_card"}})
			})}}
			for _, state := range []string{"pending", "approved", "rejected", "canceled", "expired"} {
				card := &sdk.InteractionCard{RequestID: "approval_dynamic", Revision: 7, Kind: "tool_approval", State: state, Title: "操作审批", Summary: "legacy text"}
				// Decode the additive host field so the old implementation fails on behavior.
				details, _ := json.Marshal(map[string]any{"tool_approval": map[string]any{"tool_name": tool, "risk_level": "dangerous", "arguments_preview": "{\n  \"secret\": \"[REDACTED]\",\n  \"value\": \"```<at id=all>\"\n}"}})
				if err := json.Unmarshal(details, card); err != nil {
					t.Fatal(err)
				}
				if state == "pending" {
					card.Actions = []sdk.InteractionButton{{ID: "approve", Label: "批准"}, {ID: "reject", Label: "拒绝"}}
				} else {
					card.MessageID = "om_card"
				}
				result, err := NewConnector().Send(context.Background(), runtime, sdk.OutboundMessage{AccountUUID: account.UUID, ChatID: "oc_chat", MessageUUID: "im_dynamic_" + state, Interaction: card})
				if err != nil || result == nil || result.MessageID != "om_card" {
					t.Fatalf("%s send: %#v %v", state, result, err)
				}
				var content map[string]any
				if err := json.Unmarshal([]byte(payload["content"].(string)), &content); err != nil {
					t.Fatal(err)
				}
				if content["schema"] != "2.0" {
					t.Fatalf("%s requires native schema 2.0: %v", state, content)
				}
				encoded, _ := json.Marshal(content["body"])
				rendered := string(encoded) + content["body"].(map[string]any)["elements"].([]any)[0].(map[string]any)["content"].(string)
				if !strings.Contains(rendered, "[REDACTED]") || strings.Contains(rendered, "legacy text") {
					t.Fatalf("missing structured detail: %s", rendered)
				}
				if strings.Contains(rendered, "<at") || strings.Contains(rendered, "```unsafe") {
					t.Fatalf("markup injection: %s", rendered)
				}
				color := map[string]string{"pending": "orange", "approved": "green", "rejected": "red", "canceled": "grey", "expired": "grey"}[state]
				if content["header"].(map[string]any)["template"] != color {
					t.Fatalf("wrong %s status color", state)
				}
				if state == "pending" {
					if !strings.Contains(rendered, "可复用授权") || strings.Contains(rendered, "仅对本次请求生效") || method != "POST" || !strings.Contains(rendered, `"behaviors":[{"type":"callback"`) || !strings.Contains(rendered, "approval_dynamic:7:approve") || !strings.Contains(rendered, "approval_dynamic:7:reject") || !strings.Contains(rendered, "批准本次操作") {
						t.Fatalf("pending actions: %s %s", method, rendered)
					}
				} else if strings.Contains(rendered, "请先拒绝") || method != "PATCH" || !strings.HasSuffix(path, "/om_card") || strings.Contains(rendered, `"tag":"button"`) || strings.Contains(rendered, "beak_action") {
					t.Fatalf("terminal update: %s %s %s", method, path, rendered)
				}
			}
			for _, risk := range []string{"", "future_risk"} {
				raw, _ := json.Marshal(map[string]any{"request_id": "approval_long", "revision": 1, "kind": "tool_approval", "state": "pending", "title": "审批", "tool_approval": map[string]any{"tool_name": tool, "risk_level": risk, "arguments_preview": strings.Repeat("长参数", 3000)}})
				var card sdk.InteractionCard
				if err := json.Unmarshal(raw, &card); err != nil {
					t.Fatal(err)
				}
				_, err := NewConnector().Send(context.Background(), runtime, sdk.OutboundMessage{AccountUUID: account.UUID, ChatID: "oc_chat", MessageUUID: "im_long_" + risk, Interaction: &card})
				if err != nil {
					t.Fatal(err)
				}
				content := payload["content"].(string)
				if len(content) > 16000 || !strings.Contains(content, "已截断") || strings.Contains(content, "高风险") {
					t.Fatalf("long or unknown details misrepresented: %d bytes", len(content))
				}
			}
		})
	}
}
