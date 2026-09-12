package edgecache

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

const DefaultBaseURL = "https://api.cloudflare.com/client/v4"

type Client struct {
	HTTP    *http.Client
	BaseURL string
	ZoneID  string
	Token   string
}

type apiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type purgeResponse struct {
	Success bool       `json:"success"`
	Errors  []apiError `json:"errors"`
}

func (c *Client) PurgeHosts(ctx context.Context, hosts []string) error {
	if len(hosts) == 0 {
		return errors.New("edgecache: no hosts to purge")
	}
	payload, err := json.Marshal(map[string][]string{"hosts": hosts})
	if err != nil {
		return err
	}
	base := c.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	url := strings.TrimRight(base, "/") + "/zones/" + c.ZoneID + "/purge_cache"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")

	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("edgecache: purge request: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))

	var body purgeResponse
	_ = json.Unmarshal(raw, &body)
	if resp.StatusCode/100 == 2 && body.Success {
		return nil
	}
	return fmt.Errorf("edgecache: purge failed: status=%d %s", resp.StatusCode, describe(body.Errors, raw))
}

const errorBodySnippet = 256

func describe(errs []apiError, raw []byte) string {
	if len(errs) == 0 {
		snippet := strings.TrimSpace(string(raw))
		if len(snippet) > errorBodySnippet {
			snippet = snippet[:errorBodySnippet]
		}
		if snippet == "" {
			return "no error detail"
		}
		return "body=" + strconv.Quote(snippet)
	}
	parts := make([]string, 0, len(errs))
	for _, e := range errs {
		parts = append(parts, fmt.Sprintf("%d %s", e.Code, e.Message))
	}
	return strings.Join(parts, "; ")
}
