package dingtalk

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

type Client struct {
	webhook   string
	secret    string
	httpClient *http.Client
}

type Response struct {
	ErrCode int    `json:"errcode"`
	ErrMsg  string `json:"errmsg"`
}

func NewClient(webhook, secret string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &Client{
		webhook: webhook,
		secret:  secret,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

func (c *Client) SendMarkdown(ctx context.Context, title, markdown string) (*Response, error) {
	if c.webhook == "" {
		return nil, fmt.Errorf("dingtalk webhook is empty")
	}

	payload := map[string]any{
		"msgtype": "markdown",
		"markdown": map[string]string{
			"title": title,
			"text":  markdown,
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	endpoint, err := c.signedURL()
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	var out Response
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &out, nil
}

func (c *Client) signedURL() (string, error) {
	if c.secret == "" {
		return c.webhook, nil
	}

	ts := time.Now().UnixMilli()
	signature := sign(fmt.Sprintf("%d\n%s", ts, c.secret), c.secret)

	u, err := url.Parse(c.webhook)
	if err != nil {
		return "", fmt.Errorf("invalid webhook url: %w", err)
	}
	q := u.Query()
	q.Set("timestamp", fmt.Sprintf("%d", ts))
	q.Set("sign", signature)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func sign(message, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(message))
	sum := mac.Sum(nil)
	return base64.StdEncoding.EncodeToString(sum)
}
