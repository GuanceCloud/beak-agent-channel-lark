package beaklark

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/GuanceCloud/beak-agent-channel-lark/sdk"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

func sendLarkInteraction(ctx context.Context, runtime sdk.Runtime, req sdk.OutboundMessage) (*sdk.SendResult, error) {
	account, err := selectRuntimeAccount(runtime, req.AccountUUID)
	if err != nil {
		return nil, err
	}
	client := clientFromAccount(runtime, account)
	id, err := client.SendInteraction(ctx, req.ChatID, req.MessageUUID, *req.Interaction)
	if err != nil {
		return nil, err
	}
	return &sdk.SendResult{Platform: effectiveAccountPlatform(runtime, account), AccountUUID: accountKey(account), MessageID: id}, nil
}

func handleLarkInteraction(ctx context.Context, runtime sdk.Runtime, account sdk.ChannelAccount, body []byte, verifyToken bool) (bool, *callback.CardActionTriggerResponse, error) {
	var event callback.CardActionTriggerEvent
	if err := json.Unmarshal(body, &event); err != nil {
		return false, nil, err
	}
	if event.EventV2Base == nil || event.EventV2Base.Header == nil || event.EventV2Base.Header.EventType != "card.action.trigger" {
		return false, nil, nil
	}
	if verifyToken && event.EventV2Base.Header.Token != stringValue(account.Credential["verification_token"]) {
		return true, nil, fmt.Errorf("lark callback verification token mismatch")
	}
	response, err := processLarkInteraction(ctx, runtime, account, &event)
	return true, response, err
}

func processLarkInteraction(ctx context.Context, runtime sdk.Runtime, account sdk.ChannelAccount, event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
	if event == nil || event.EventV2Base == nil || event.EventV2Base.Header == nil || event.EventV2Base.Header.AppID != stringValue(account.Credential["app_id"]) || event.Event == nil {
		return nil, fmt.Errorf("lark callback application identity mismatch")
	}
	data := event.Event
	if data.Operator == nil || data.Context == nil || data.Action == nil {
		return nil, fmt.Errorf("lark callback identity missing")
	}
	token, _ := data.Action.Value["beak_action"].(string)
	action, err := sdk.ParseInteractionToken(token)
	if err != nil {
		return nil, err
	}
	if data.Action.Name == "plan_revise" || len(data.Action.FormValue) > 0 {
		reason, ok := data.Action.FormValue["reason"].(string)
		reason = strings.TrimSpace(reason)
		if action.ActionID != "revise" || len(data.Action.FormValue) != 1 || !ok || reason == "" || utf8.RuneCountInString(reason) > 1000 {
			return nil, fmt.Errorf("请填写调整说明（1–1000 字）")
		}
		action.Inputs = map[string]string{"reason": reason}
	}
	action.ActorID = data.Operator.OpenID
	action.ChatID = data.Context.OpenChatID
	action.MessageID = data.Context.OpenMessageID
	action.EventID = event.EventV2Base.Header.EventID
	if _, err := sdk.DispatchInteraction(ctx, runtime, action); err != nil {
		var access interface{ InteractionErrorCode() string }
		if errors.As(err, &access) && access.InteractionErrorCode() == "actor_not_requester" {
			return &callback.CardActionTriggerResponse{Toast: &callback.Toast{
				Type: "warning", Content: "只有本次任务的发起人可以审批，请联系发起人处理。",
			}}, nil
		}
		return nil, err
	}
	return &callback.CardActionTriggerResponse{Toast: &callback.Toast{Type: "success", Content: "已处理"}}, nil
}
