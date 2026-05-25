package sdk

import "testing"

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
