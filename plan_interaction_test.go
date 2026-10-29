package beaklark

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/GuanceCloud/beak-agent-channel-lark/sdk"
)

// Exercise the connector transport: the host integration fixture cannot inspect
// Feishu's native JSON or its authenticated form callback envelope.
func TestPlanCardFeedbackLifecycle(t *testing.T) {
	account := sdkAccount("acct-1", "cli_1", "secret_1", "")
	gateway := &interactionTestGateway{fakeSDKGateway: &fakeSDKGateway{}}
	var payload map[string]any
	runtime := sdk.Runtime{Account: account, Gateway: gateway, HTTPClient: &http.Client{Transport: testRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.Path, "tenant_access_token") {
			return testJSONResponse(map[string]any{"code": 0, "tenant_access_token": "test-token", "expire": 7200})
		}
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		return testJSONResponse(map[string]any{"code": 0, "data": map[string]string{"message_id": "om_plan"}})
	})}}
	var card sdk.InteractionCard
	if err := json.Unmarshal([]byte(`{"language":"zh","request_id":"plan_123","revision":1,"kind":"plan_confirmation","state":"pending","title":"演示计划","summary":"## 目标\n仅输出 **完成**。 <at id=all>不应提醒全员</at>","plan_confirmation":{"feedback_enabled":true},"actions":[{"id":"approve","label":"批准计划"},{"id":"reject","label":"拒绝计划"},{"id":"revise","label":"要求修改"}]}`), &card); err != nil {
		t.Fatal(err)
	}
	_, err := NewConnector().Send(context.Background(), runtime, sdk.OutboundMessage{AccountUUID: account.UUID, ChatID: "oc_chat", MessageUUID: "plan_demo", Interaction: &card})
	if err != nil {
		t.Fatal(err)
	}
	content := payload["content"].(string)
	var decoded map[string]any
	if err := json.Unmarshal([]byte(content), &decoded); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(decoded["body"])
	for _, want := range []string{`"tag":"markdown"`, `"tag":"form"`, `"name":"reason"`, `"required":true`, `"form_action_type":"submit"`, "plan_123:1:revise", "计划确认 · 待确认"} {
		if !strings.Contains(content, want) {
			t.Errorf("missing %s in %s", want, content)
		}
	}
	if strings.Contains(string(body), "<at") {
		t.Fatal("plan body can inject mentions")
	}
	for _, tt := range []struct {
		values string
		valid  bool
	}{
		{`{"reason":"第二步改为只读检查"}`, true}, {`{"reason":"  "}`, false}, {`{"reason":12}`, false}, {`{"unknown":"x"}`, false},
	} {
		event := `{"schema":"2.0","header":{"event_id":"plan-event","event_type":"card.action.trigger","app_id":"cli_1","token":"verify-token"},"event":{"operator":{"open_id":"ou_actor"},"context":{"open_chat_id":"oc_chat","open_message_id":"om_plan"},"action":{"name":"plan_revise","value":{"beak_action":"plan_123:1:revise"},"form_value":` + tt.values + `}}}`
		_, err := (Connector{}).HandleEvent(context.Background(), runtime, account, []byte(event))
		if (err == nil) != tt.valid {
			t.Fatalf("form %s: %v", tt.values, err)
		}
		if tt.valid && gateway.action.Inputs["reason"] != "第二步改为只读检查" {
			t.Fatal("form feedback did not reach host")
		}
	}
	// Persisted explicit action distinguishes revision from refusal, although the
	// old terminal wire state remains rejected for other released connectors.
	raw := `{"state":"rejected","message_id":"om_plan","actions":[],"plan_confirmation":{"decision_action":"revise","feedback":"第二步改为只读检查"}}`
	if err := json.Unmarshal([]byte(raw), &card); err != nil {
		t.Fatal(err)
	}
	_, err = NewConnector().Send(context.Background(), runtime, sdk.OutboundMessage{AccountUUID: account.UUID, ChatID: "oc_chat", Interaction: &card})
	if err != nil {
		t.Fatal(err)
	}
	content = payload["content"].(string)
	if !strings.Contains(content, "已要求修改") || !strings.Contains(content, "第二步改为只读检查") || strings.Contains(content, `"tag":"button"`) {
		t.Fatalf("wrong terminal card: %s", content)
	}
}
