package lark

import (
	"encoding/json"
	"github.com/GuanceCloud/beak-agent-channel-lark/internal/locale"
	"html"
	"strings"

	"github.com/GuanceCloud/beak-agent-channel-lark/sdk"
)

// Layout follows the approved CardKit JSON 2.0 design. The host owns the
// persisted request, decisions and redaction; this renderer only formats it.
func renderToolApproval(card sdk.InteractionCard) ([]byte, error) {
	tr := locale.Translator(card.Language)
	label, color, introduction := approvalPresentation(card.Language, card.State)
	detail := card.ToolApproval
	if detail.ResolutionText != "" && (card.State == "canceled" || card.State == "expired") {
		introduction += "\n" + escapeCardMarkdown(detail.ResolutionText)
	}
	risk, riskColor := approvalRiskPresentation(card.Language, detail.RiskLevel)
	tool, _ := boundedCardText(detail.ToolName, 200)
	parameters, truncated := boundedCardText(detail.ArgumentsPreview, 1200)
	if parameters == "" {
		parameters = tr("No arguments provided")
	}
	// Parameters are JSON display text. Escape fence/HTML delimiters as JSON
	// unicode escapes so arbitrary argument strings cannot inject card markup.
	parameters = strings.NewReplacer("`", `\u0060`, "<", `\u003c`, ">", `\u003e`, "&", `\u0026`).Replace(parameters)
	content := introduction + tr("\n\n**Tool** ") + escapeCardMarkdown(tool) +
		tr("\n**Risk level** <text_tag color=\"") + riskColor + "\">" + risk + "</text_tag>" +
		tr("\n\n**Arguments**\n```json\n") + parameters + "\n```"
	if truncated || detail.ArgumentsTruncated {
		content += tr("\n<font color=\"grey\">Argument preview truncated. Reject and ask for clarification if the operation is unclear.</font>")
	}
	if card.State == "pending" {
		content += tr("\n<font color=\"grey\">Sensitive values are redacted. If the impact is unclear, reject and ask the Agent for details.</font>")
	}
	elements := []any{approvalMarkdown(content)}
	buttons := []any{}
	for _, action := range card.Actions {
		token, err := card.ButtonToken(action)
		if err != nil {
			return nil, err
		}
		text, style := action.Label, "default"
		if action.ID == "approve" {
			text, style = tr("Approve this operation"), "primary_filled"
		}
		buttons = append(buttons, map[string]any{
			"tag": "button", "text": map[string]string{"tag": "plain_text", "content": text},
			"type": style, "width": "default", "size": "medium",
			"behaviors": []any{map[string]any{"type": "callback", "value": map[string]string{"beak_action": token}}},
		})
	}
	if len(buttons) > 0 {
		elements = append(elements, map[string]any{"tag": "column_set", "columns": []any{
			map[string]any{"tag": "column", "width": "auto", "direction": "horizontal", "vertical_align": "top", "elements": buttons},
		}})
	}
	footer := tr("This approval has ended. No further action is needed. Sensitive values remain redacted.")
	if card.State == "pending" {
		footer = tr("Your approval may be reused for the same tool and arguments or explicit scope in this session. Group members do not share authorization. Full access is not enabled.")
	}
	elements = append(elements, approvalMarkdown("<font color=\"grey\">"+footer+"</font>"))
	return json.Marshal(map[string]any{
		"schema": "2.0",
		"config": map[string]any{"update_multi": true, "style": map[string]any{
			"text_size": map[string]any{"normal": map[string]string{"default": "normal", "pc": "normal", "mobile": "heading"}},
		}},
		"header": map[string]any{
			"title":    map[string]string{"tag": "plain_text", "content": tr("Tool approval · ") + label},
			"subtitle": map[string]string{"tag": "plain_text", "content": tr("Beak · Tool authorization")},
			"template": color, "padding": "12px 12px 12px 12px",
		},
		"body": map[string]any{"direction": "vertical", "padding": "12px 12px 12px 12px", "elements": elements},
	})
}

func approvalPresentation(language, state string) (label, color, introduction string) {
	tr := locale.Translator(language)
	switch state {
	case "approved":
		return tr("Approved"), "green", tr("**Operation authorized**\nApproval permits execution. Results will follow separately.")
	case "rejected":
		return tr("Rejected"), "red", tr("**Operation not authorized**\nThis tool call will not execute. Ask the Agent for clarification before continuing.")
	case "canceled":
		return tr("Canceled"), "grey", tr("**Approval canceled**\nThis request has ended and cannot be acted on.")
	case "expired":
		return tr("Expired"), "grey", tr("**Approval expired**\nSend a new request to the Agent to continue.")
	default:
		return tr("Awaiting confirmation"), "orange", tr("The Agent requests a tool call. Review the details before deciding.")
	}
}

func approvalRiskPresentation(language, risk string) (string, string) {
	tr := locale.Translator(language)
	switch strings.ToLower(strings.TrimSpace(risk)) {
	case "dangerous", "high":
		return tr("High risk"), "red"
	case "panic":
		return tr("Execution prohibited"), "red"
	case "medium":
		return tr("Medium risk"), "orange"
	case "low", "safe":
		return tr("Low risk"), "green"
	default:
		return tr("Unspecified"), "grey"
	}
}

func approvalMarkdown(content string) map[string]any {
	return map[string]any{"tag": "markdown", "content": content, "text_align": "left", "text_size": "normal", "margin": "0px 0px 0px 0px"}
}

func boundedCardText(text string, limit int) (string, bool) {
	runes := []rune(text)
	if len(runes) > limit {
		return string(runes[:limit]), true
	}
	return text, false
}

func escapeCardMarkdown(text string) string {
	return strings.NewReplacer("\\", "\\\\", "`", "\\`", "*", "\\*", "_", "\\_", "[", "\\[", "]", "\\]", "~", "\\~", "\n", " ", "\r", " ").Replace(html.EscapeString(text))
}
