package lark

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultRequestTimeout = 15 * time.Second

type Client struct {
	BaseURL        string
	AppID          string
	AppSecret      string
	TenantToken    string
	RequestTimeout time.Duration
	HTTPClient     *http.Client
}

func NewClient(baseURL, appID, appSecret string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		BaseURL:        baseURL,
		AppID:          appID,
		AppSecret:      appSecret,
		RequestTimeout: defaultRequestTimeout,
		HTTPClient:     http.DefaultClient,
	}
}

func (c *Client) TenantAccessToken(ctx context.Context) (string, error) {
	if strings.TrimSpace(c.AppID) == "" {
		return "", fmt.Errorf("app_id is required")
	}
	if strings.TrimSpace(c.AppSecret) == "" {
		return "", fmt.Errorf("app_secret is required")
	}
	var resp TokenResponse
	if err := c.doJSON(ctx, http.MethodPost, "/auth/v3/tenant_access_token/internal", nil, map[string]string{
		"app_id":     c.AppID,
		"app_secret": c.AppSecret,
	}, &resp); err != nil {
		return "", err
	}
	if resp.Code != 0 {
		return "", fmt.Errorf("tenant access token failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	if strings.TrimSpace(resp.TenantAccessToken) == "" {
		return "", fmt.Errorf("tenant access token failed: missing token")
	}
	return resp.TenantAccessToken, nil
}

func (c *Client) SendText(ctx context.Context, req SendTextRequest) (*SendTextResponse, error) {
	if strings.TrimSpace(req.ReceiveID) == "" {
		return nil, fmt.Errorf("receive_id is required")
	}
	if strings.TrimSpace(req.Text) == "" {
		return nil, fmt.Errorf("text is required")
	}
	receiveIDType := strings.TrimSpace(req.ReceiveIDType)
	if receiveIDType == "" {
		receiveIDType = ReceiveIDTypeForTarget(req.ReceiveID)
	}
	token := strings.TrimSpace(c.TenantToken)
	if token == "" {
		var err error
		token, err = c.TenantAccessToken(ctx)
		if err != nil {
			return nil, err
		}
	}
	content, err := json.Marshal(map[string]string{"text": req.Text})
	if err != nil {
		return nil, err
	}
	body := map[string]any{
		"receive_id": req.ReceiveID,
		"msg_type":   "text",
		"content":    string(content),
	}
	if req.UUID != "" {
		body["uuid"] = req.UUID
	}
	var resp SendTextResponse
	if err := c.doJSON(ctx, http.MethodPost, "/im/v1/messages", map[string]string{"receive_id_type": receiveIDType}, body, &resp, withBearer(token)); err != nil {
		return nil, err
	}
	if resp.Code != 0 {
		return nil, fmt.Errorf("send text failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return &resp, nil
}

type requestOption func(*http.Request)

func withBearer(token string) requestOption {
	return func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

func (c *Client) doJSON(ctx context.Context, method, path string, query map[string]string, body any, out any, opts ...requestOption) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		reader = bytes.NewReader(data)
	}
	timeout := c.RequestTimeout
	if timeout <= 0 {
		timeout = defaultRequestTimeout
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, method, c.url(path, query), reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "BeakAgentLark/0.1.0")
	if body != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	for _, opt := range opts {
		opt(req)
	}
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s failed: status=%d body=%s", method, path, resp.StatusCode, string(data))
	}
	if out == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func (c *Client) url(path string, query map[string]string) string {
	base := strings.TrimRight(c.BaseURL, "/")
	base = strings.TrimSuffix(base, "/open-apis")
	values := url.Values{}
	for key, value := range query {
		if value != "" {
			values.Set(key, value)
		}
	}
	out := base + "/open-apis/" + strings.TrimLeft(path, "/")
	if encoded := values.Encode(); encoded != "" {
		out += "?" + encoded
	}
	return out
}
