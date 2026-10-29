package lark

import (
	"encoding/json"
	"github.com/GuanceCloud/beak-agent-channel-lark/sdk"
	"strings"
	"testing"
)

func TestApprovalLanguageDoesNotChangeArgumentsOrTokens(t *testing.T) {
	for _, tc := range []struct{ language, label string }{{"en", "Tool approval"}, {"zh", "操作审批"}} {
		card := sdk.InteractionCard{RequestID: "approval_locale", Kind: "tool_approval", State: "pending", ToolApproval: &sdk.ToolApprovalDetails{ToolName: "exec", ArgumentsPreview: `{"command":"echo 中文"}`}, Actions: []sdk.InteractionButton{{ID: "approve", Label: "Approve"}}}
		raw, _ := json.Marshal(map[string]string{"language": tc.language})
		if err := json.Unmarshal(raw, &card); err != nil {
			t.Fatal(err)
		}
		rendered, err := renderToolApproval(card)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{tc.label, "echo 中文", "approval_locale:0:approve"} {
			if !strings.Contains(string(rendered), want) {
				t.Fatalf("missing %q: %s", want, rendered)
			}
		}
	}
}
