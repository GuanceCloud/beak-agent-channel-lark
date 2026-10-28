package lark

import (
	"encoding/json"
	"html"
	"strings"

	"github.com/GuanceCloud/beak-agent-channel-lark/sdk"
)

func renderPlanConfirmation(card sdk.InteractionCard) ([]byte, error) {
	label, color, notice := planPresentation(card)
	elements := []any{
		approvalMarkdown("**" + escapeCardMarkdown(card.Title) + "**"),
		approvalMarkdown(notice),
		map[string]any{"tag": "hr"},
		approvalMarkdown(planMarkdown(card.Summary)),
	}
	buttons := []any{}
	var revisionForm any
	for _, action := range card.Actions {
		token, err := card.ButtonToken(action)
		if err != nil {
			return nil, err
		}
		style := "default"
		text := action.Label
		switch action.ID {
		case "approve":
			text, style = "批准计划", "primary_filled"
		case "reject":
			text = "拒绝计划"
		case "revise":
			text = "要求修改"
		}
		button := map[string]any{
			"tag": "button", "text": map[string]string{"tag": "plain_text", "content": text},
			"type": style, "behaviors": []any{map[string]any{"type": "callback", "value": map[string]string{"beak_action": token}}},
		}
		if action.ID == "revise" && card.PlanConfirmation != nil && card.PlanConfirmation.FeedbackEnabled {
			button["name"], button["form_action_type"] = "plan_revise", "submit"
			revisionForm = map[string]any{"tag": "form", "name": "plan_revision", "elements": []any{
				approvalMarkdown("**要求修改**\n填写调整说明后提交，Agent 将重新生成计划，等待你再次确认。"),
				map[string]any{"tag": "input", "name": "reason", "required": true, "input_type": "multiline_text", "max_length": 1000, "width": "fill", "placeholder": map[string]string{"tag": "plain_text", "content": "输入调整说明"}},
				button,
			}}
		} else {
			buttons = append(buttons, button)
		}
	}
	if len(buttons) > 0 {
		elements = append(elements, map[string]any{"tag": "column_set", "columns": []any{map[string]any{"tag": "column", "width": "auto", "direction": "horizontal", "elements": buttons}}})
	}
	if revisionForm != nil {
		elements = append(elements, map[string]any{"tag": "hr"}, revisionForm)
	}
	if detail := card.PlanConfirmation; detail != nil && detail.Feedback != "" {
		elements = append(elements, approvalMarkdown("**调整说明**\n"+planMarkdown(detail.Feedback)))
	}
	return json.Marshal(map[string]any{
		"schema": "2.0", "config": map[string]any{"update_multi": true},
		"header": map[string]any{"title": map[string]string{"tag": "plain_text", "content": "计划确认 · " + label}, "subtitle": map[string]string{"tag": "plain_text", "content": "Beak · 核对计划后决定下一步"}, "template": color},
		"body":   map[string]any{"direction": "vertical", "padding": "12px 12px 12px 12px", "elements": elements},
	})
}

func planPresentation(card sdk.InteractionCard) (string, string, string) {
	if d := card.PlanConfirmation; d != nil && d.DecisionAction == "revise" && card.State == "rejected" {
		if d.AwaitingRevisionInput {
			return "待补充修改意见", "orange", "请在当前聊天中发送调整说明，收到后将重新生成计划。"
		}
		return "已要求修改", "orange", "调整说明已提交。旧计划不会执行，请等待并确认新的计划。"
	}
	switch card.State {
	case "approved":
		return "已批准", "green", "计划已获批准，执行结果将另行回复。"
	case "rejected":
		return "已拒绝", "red", "计划已拒绝，不会执行。"
	case "canceled":
		return "已取消", "grey", "此计划已失效，请查看最新计划。"
	case "expired":
		return "已过期", "grey", "此计划已过期，请重新发起。"
	default:
		return "待确认", "blue", "请核对计划内容。批准后开始执行；需要调整时请填写下方说明并提交。"
	}
}

// Feishu Markdown does not render ATX headings. Convert only display syntax,
// keeping lists, emphasis and code blocks; escape HTML to prevent @all markup.
func planMarkdown(content string) string {
	lines := strings.Split(html.EscapeString(content), "\n")
	inCode := false
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inCode = !inCode
			continue
		}
		if inCode {
			continue
		}
		title := strings.TrimLeft(line, "#")
		level := len(line) - len(title)
		if level > 0 && level <= 6 && strings.HasPrefix(title, " ") {
			lines[i] = "**" + strings.TrimSpace(title) + "**"
		}
	}
	return strings.Join(lines, "\n")
}
