package sdk

import "testing"

func TestInteractionLanguage(t *testing.T) {
	for _, platform := range []string{"feishu", "dingtalk", "wecom", "weixin", "wechat", "lark", "teams", "slack", "telegram", "unknown"} {
		fallback := "en"
		switch platform {
		case "feishu", "dingtalk", "wecom", "weixin", "wechat":
			fallback = "zh"
		}
		for _, tc := range []struct{ input, want string }{{"", fallback}, {"und", fallback}, {"fr", fallback}, {"zh-CN", "zh"}, {"zh-TW", "zh"}, {" EN_us ", "en"}} {
			if got := ResolveInteractionLanguage(platform, tc.input); got != tc.want {
				t.Errorf("%s/%q: %s, want %s", platform, tc.input, got, tc.want)
			}
		}
	}
}
