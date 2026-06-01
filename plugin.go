package beaklark

import "context"

const (
	ID       = "beak-agent-lark"
	Platform = "lark"
)

type API interface {
	RegisterChannel(Channel) error
}

type Plugin struct{}

type Channel struct{}

type Metadata struct {
	ID          string
	Platform    string
	Label       string
	Description string
}

type Capabilities struct {
	DirectChat     bool
	GroupChat      bool
	Text           bool
	Media          bool
	BlockStreaming bool
}

type SettingsSchema struct {
	Type                 string         `json:"type"`
	AdditionalProperties bool           `json:"additionalProperties"`
	Properties           map[string]any `json:"properties"`
	Required             []string       `json:"required,omitempty"`
}

func New() Plugin {
	return Plugin{}
}

func Register(api API) error {
	return New().Register(api)
}

func (Plugin) Register(api API) error {
	return api.RegisterChannel(Channel{})
}

func (Plugin) Channel() Channel {
	return Channel{}
}

func (Channel) Metadata() Metadata {
	return Metadata{
		ID:          ID,
		Platform:    Platform,
		Label:       "Lark/Feishu",
		Description: "Lark/Feishu connector for Beak channel gateway sessions",
	}
}

func (Channel) Capabilities() Capabilities {
	return Capabilities{
		DirectChat:     true,
		GroupChat:      true,
		Text:           true,
		Media:          false,
		BlockStreaming: true,
	}
}

func (Channel) SettingsSchema() SettingsSchema {
	return SettingsSchema{
		Type:                 "object",
		AdditionalProperties: false,
		Required:             []string{"app_id", "app_secret"},
		Properties: map[string]any{
			"app_id": map[string]any{
				"type":        "string",
				"title":       "App ID",
				"description": "Lark/Feishu self-built application app_id.",
			},
			"app_secret": map[string]any{
				"type":        "string",
				"title":       "App Secret",
				"description": "Lark/Feishu self-built application app_secret.",
				"secret":      true,
			},
			"verification_token": map[string]any{
				"type":        "string",
				"title":       "Verification Token",
				"description": "Optional event subscription verification token.",
				"secret":      true,
			},
			"encrypt_key": map[string]any{
				"type":        "string",
				"title":       "Encrypt Key",
				"description": "Optional event subscription encrypt key. Used for encrypted webhook payload decryption and signature verification.",
				"secret":      true,
			},
			"brand": map[string]any{
				"type":        "string",
				"title":       "Brand",
				"description": "feishu or lark. Defaults to feishu.",
			},
			"bot_open_id": map[string]any{
				"type":        "string",
				"title":       "Bot Open ID",
				"description": "Optional bot open_id used to drop self echo messages.",
			},
		},
	}
}

func (Channel) CheckHealth(context.Context) error {
	return nil
}
