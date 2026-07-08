package sdk

import (
	"encoding/json"
	"testing"
)

func TestIdentityHelpers(t *testing.T) {
	if got := ChatSourceID("lark", "account-1", "group", "chat-1"); got != "lark:account-1:group:chat-1" {
		t.Fatalf("source id=%q", got)
	}
	if got := IMPersonParticipantID("lark", "direct", "chat-1", "user-1"); got != "im:lark:direct:chat-1:user:user-1" {
		t.Fatalf("participant=%q", got)
	}
	if got := BridgeParticipantID("lark"); got != "bridge:lark" {
		t.Fatalf("bridge=%q", got)
	}
}

func TestOutboundMessageCommonFormatContract(t *testing.T) {
	data, err := json.Marshal(OutboundMessage{
		Text:     "# 日志查询\n- 错误日志",
		Format:   "markdown",
		Title:    "日志查询",
		ThreadID: "thread-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["text"] != "# 日志查询\n- 错误日志" || decoded["format"] != "markdown" || decoded["title"] != "日志查询" || decoded["thread_id"] != "thread-1" {
		t.Fatalf("common outbound json=%+v", decoded)
	}
}

func TestOutboundAckCommonContract(t *testing.T) {
	data, err := json.Marshal(OutboundAck{
		AccountUUID:       "account-1",
		SessionUUID:       "session-1",
		SourceMessageUUID: "message-1",
		ChatType:          ChatTypeGroup,
		ChatID:            "chat-1",
		TargetMessageID:   "om_1",
		Intent:            "processing",
		Action:            "start",
		Mode:              "auto",
		Emoji:             "thinking",
	})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["target_message_id"] != "om_1" || decoded["intent"] != "processing" || decoded["action"] != "start" || decoded["emoji"] != "thinking" {
		t.Fatalf("common outbound ack json=%+v", decoded)
	}
}
