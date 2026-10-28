package lark

import (
	"strings"
	"testing"

	"github.com/GuanceCloud/beak-agent-channel-lark/sdk"
)

func TestToolApprovalCardShowsResolutionReason(t *testing.T) {
	card := sdk.InteractionCard{State: "canceled", ToolApproval: &sdk.ToolApprovalDetails{ToolName: "exec", ResolutionText: "Agent 已重启"}}
	data, err := renderToolApproval(card)
	if err != nil || !strings.Contains(string(data), "Agent 已重启") {
		t.Fatalf("card = %s, error = %v", data, err)
	}
}
