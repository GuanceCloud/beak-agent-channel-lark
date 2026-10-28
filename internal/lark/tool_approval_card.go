package lark

import (
	"encoding/json"
	"html"
	"strings"

	"github.com/GuanceCloud/beak-agent-channel-lark/sdk"
)

// Layout follows the approved CardKit JSON 2.0 design. The host owns the
// persisted request, decisions and redaction; this renderer only formats it.
func renderToolApproval(card sdk.InteractionCard) ([]byte, error) {
	label, color, introduction := approvalPresentation(card.State)
	detail := card.ToolApproval
	if detail.ResolutionText != "" && (card.State == "canceled" || card.State == "expired") {
		introduction += "\n" + escapeCardMarkdown(detail.ResolutionText)
	}
	risk, riskColor := approvalRiskPresentation(detail.RiskLevel)
	tool, _ := boundedCardText(detail.ToolName, 200)
	parameters, truncated := boundedCardText(detail.ArgumentsPreview, 1200)
	if parameters == "" {
		parameters = "未提供参数"
	}
	// Parameters are JSON display text. Escape fence/HTML delimiters as JSON
	// unicode escapes so arbitrary argument strings cannot inject card markup.
	parameters = strings.NewReplacer("`", `\u0060`, "<", `\u003c`, ">", `\u003e`, "&", `\u0026`).Replace(parameters)
	content := introduction + "\n\n**调用工具**　" + escapeCardMarkdown(tool) +
		"\n**风险等级**　<text_tag color=\"" + riskColor + "\">" + risk + "</text_tag>" +
		"\n\n**执行参数**\n```json\n" + parameters + "\n```"
	if truncated || detail.ArgumentsTruncated {
		content += "\n<font color=\"grey\">参数预览已截断，请在 Beak 会话中核对完整信息。</font>"
	}
	if card.State == "pending" {
		content += "\n<font color=\"grey\">参数已脱敏。若无法判断操作影响，请先拒绝并要求 Agent 补充说明。</font>"
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
			text, style = "批准本次操作", "primary_filled"
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
	footer := "本次审批已结束，无需重复操作。敏感参数保持脱敏展示。"
	if card.State == "pending" {
		footer = "当前会话中，您的相同工具和参数或显式审批范围可复用授权；不同群成员不共享授权，不会切换为完全访问权限。"
	}
	elements = append(elements, approvalMarkdown("<font color=\"grey\">"+footer+"</font>"))
	return json.Marshal(map[string]any{
		"schema": "2.0",
		"config": map[string]any{"update_multi": true, "style": map[string]any{
			"text_size": map[string]any{"normal": map[string]string{"default": "normal", "pc": "normal", "mobile": "heading"}},
		}},
		"header": map[string]any{
			"title":    map[string]string{"tag": "plain_text", "content": "操作审批 · " + label},
			"subtitle": map[string]string{"tag": "plain_text", "content": "Beak · 工具调用授权"},
			"template": color, "padding": "12px 12px 12px 12px",
		},
		"body": map[string]any{"direction": "vertical", "padding": "12px 12px 12px 12px", "elements": elements},
	})
}

func approvalPresentation(state string) (label, color, introduction string) {
	switch state {
	case "approved":
		return "已批准", "green", "**本次操作已获授权**\n审批通过仅代表允许执行，执行结果将另行回复。"
	case "rejected":
		return "已拒绝", "red", "**本次操作未获授权**\n该工具调用不会执行。如需继续，请先向 Agent 补充说明。"
	case "canceled":
		return "已取消", "grey", "**本次审批已取消**\n该请求已结束，无法继续操作。"
	case "expired":
		return "已过期", "grey", "**本次审批已过期**\n如需继续，请向 Agent 重新发起请求。"
	default:
		return "待确认", "orange", "Agent 请求调用工具，请核对以下信息后决定是否允许本次操作。"
	}
}

func approvalRiskPresentation(risk string) (string, string) {
	switch strings.ToLower(strings.TrimSpace(risk)) {
	case "dangerous", "high":
		return "高风险", "red"
	case "panic":
		return "禁止执行", "red"
	case "medium":
		return "中风险", "orange"
	case "low", "safe":
		return "低风险", "green"
	default:
		return "未标注", "grey"
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
