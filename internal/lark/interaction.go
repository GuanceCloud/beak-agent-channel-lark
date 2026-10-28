package lark

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/GuanceCloud/beak-agent-channel-lark/sdk"
)

func (c *Client) SendInteraction(ctx context.Context, chatID, messageUUID string, card sdk.InteractionCard) (string, error) {
	if err := card.Validate(); err != nil {
		return "", err
	}
	if len(card.Inputs) > 0 {
		return "", fmt.Errorf("lark native input forms are unavailable")
	}
	content, err := renderInteraction(card)
	if err != nil {
		return "", err
	}
	if card.MessageID == "" {
		result, err := c.SendMessage(ctx, SendMessageRequest{ReceiveID: chatID, MsgType: "interactive", Content: string(content), UUID: messageUUID})
		if err != nil {
			return "", err
		}
		return result.Data.MessageID, nil
	}
	token, err := c.tokenForRequest(ctx)
	if err != nil {
		return "", err
	}
	var response struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := c.doJSON(ctx, http.MethodPatch, "/im/v1/messages/"+url.PathEscape(card.MessageID), nil, map[string]string{"content": string(content)}, &response, withBearer(token)); err != nil {
		return "", err
	}
	if response.Code != 0 {
		return "", fmt.Errorf("lark card update failed: code=%d msg=%s", response.Code, response.Msg)
	}
	return card.MessageID, nil
}

// Keep non-tool interactions and pre-structured hosts compatible. Approval
// rendering uses explicit fields only, never extracts data from Summary.
func renderInteraction(card sdk.InteractionCard) ([]byte, error) {
	if card.Kind == "plan_confirmation" {
		return renderPlanConfirmation(card)
	}
	if card.Kind == "tool_approval" && card.ToolApproval != nil {
		return renderToolApproval(card)
	}
	elements := []any{map[string]any{"tag": "div", "text": map[string]string{"tag": "plain_text", "content": card.Summary}}}
	actions := []any{}
	for _, button := range card.Actions {
		token, err := card.ButtonToken(button)
		if err != nil {
			return nil, err
		}
		actions = append(actions, map[string]any{"tag": "button", "text": map[string]string{"tag": "plain_text", "content": button.Label}, "value": map[string]string{"beak_action": token}})
	}
	if len(actions) > 0 {
		elements = append(elements, map[string]any{"tag": "action", "actions": actions})
	}
	return json.Marshal(map[string]any{"config": map[string]bool{"wide_screen_mode": true, "update_multi": true}, "header": map[string]any{"title": map[string]string{"tag": "plain_text", "content": card.Title}}, "elements": elements})

}
