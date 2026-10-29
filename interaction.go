package beaklark

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/GuanceCloud/beak-agent-channel-lark/internal/locale"
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
	card := *req.Interaction
	card.Language = sdk.ResolveInteractionLanguage(effectiveAccountPlatform(runtime, account), card.Language)
	id, err := client.SendInteraction(ctx, req.ChatID, req.MessageUUID, card)
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
			return nil, fmt.Errorf("revision details must contain 1-1000 characters")
		}
		action.Inputs = map[string]string{"reason": reason}
	}
	action.ActorID = data.Operator.OpenID
	action.ChatID = data.Context.OpenChatID
	action.MessageID = data.Context.OpenMessageID
	action.EventID = event.EventV2Base.Header.EventID
	result, err := sdk.DispatchInteraction(ctx, runtime, action)
	if err != nil {
		tr := locale.Translator(sdk.InteractionErrorLanguage(effectiveAccountPlatform(runtime, account), err))
		var access interface{ InteractionErrorCode() string }
		if errors.As(err, &access) && access.InteractionErrorCode() == "actor_not_requester" {
			return &callback.CardActionTriggerResponse{Toast: &callback.Toast{
				Type: "warning", Content: tr("Only the requester can decide this approval. Please contact the requester."),
			}}, nil
		}
		return nil, err
	}
	language := ""
	if result != nil && result.Card != nil {
		language = result.Card.Language
	}
	tr := locale.Translator(sdk.ResolveInteractionLanguage(effectiveAccountPlatform(runtime, account), language))
	return &callback.CardActionTriggerResponse{Toast: &callback.Toast{Type: "success", Content: tr("Processed")}}, nil
}
