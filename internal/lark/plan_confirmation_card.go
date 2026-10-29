package lark

import (
	"encoding/json"
	"github.com/GuanceCloud/beak-agent-channel-lark/internal/locale"
	"html"
	"strings"

	"github.com/GuanceCloud/beak-agent-channel-lark/sdk"
)

func renderPlanConfirmation(card sdk.InteractionCard) ([]byte, error) {
	tr := locale.Translator(card.Language)
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
			text, style = tr("Approve plan"), "primary_filled"
		case "reject":
			text = tr("Reject plan")
		case "revise":
			text = tr("Request changes")
		}
		button := map[string]any{
			"tag": "button", "text": map[string]string{"tag": "plain_text", "content": text},
			"type": style, "behaviors": []any{map[string]any{"type": "callback", "value": map[string]string{"beak_action": token}}},
		}
		if action.ID == "revise" && card.PlanConfirmation != nil && card.PlanConfirmation.FeedbackEnabled {
			button["name"], button["form_action_type"] = "plan_revise", "submit"
			revisionForm = map[string]any{"tag": "form", "name": "plan_revision", "elements": []any{
				approvalMarkdown(tr("**Request changes**\nDescribe the changes and submit. The Agent will revise the plan for your confirmation.")),
				map[string]any{"tag": "input", "name": "reason", "required": true, "input_type": "multiline_text", "max_length": 1000, "width": "fill", "placeholder": map[string]string{"tag": "plain_text", "content": tr("Describe the changes")}},
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
		elements = append(elements, approvalMarkdown(tr("**Requested changes**\n")+planMarkdown(detail.Feedback)))
	}
	return json.Marshal(map[string]any{
		"schema": "2.0", "config": map[string]any{"update_multi": true},
		"header": map[string]any{"title": map[string]string{"tag": "plain_text", "content": tr("Plan confirmation · ") + label}, "subtitle": map[string]string{"tag": "plain_text", "content": tr("Beak · Review the plan before continuing")}, "template": color},
		"body":   map[string]any{"direction": "vertical", "padding": "12px 12px 12px 12px", "elements": elements},
	})
}

func planPresentation(card sdk.InteractionCard) (string, string, string) {
	tr := locale.Translator(card.Language)
	if d := card.PlanConfirmation; d != nil && d.DecisionAction == "revise" && card.State == "rejected" {
		if d.AwaitingRevisionInput {
			return tr("Awaiting change details"), "orange", tr("Send the requested changes in this chat. The Agent will then revise the plan.")
		}
		return tr("Changes requested"), "orange", tr("Changes submitted. The old plan will not execute. Wait for the revised plan and confirm it.")
	}
	switch card.State {
	case "approved":
		return tr("Approved"), "green", tr("Plan approved. Execution results will follow separately.")
	case "rejected":
		return tr("Rejected"), "red", tr("Plan rejected. It will not execute.")
	case "canceled":
		return tr("Canceled"), "grey", tr("This plan is no longer active. Review the latest plan.")
	case "expired":
		return tr("Expired"), "grey", tr("This plan has expired. Send a new request.")
	default:
		return tr("Awaiting confirmation"), "blue", tr("Review the plan. Approval starts execution; submit the requested changes below if needed.")
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
