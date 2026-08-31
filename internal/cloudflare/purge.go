package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const (
	defaultBaseURL = "https://api.cloudflare.com/client/v4"
	purgeTimeout   = 15 * time.Second
	maxResponse    = 1 << 20
)

type PurgeClient struct {
	ZoneID  string
	Token   string
	BaseURL string
	HTTP    *http.Client
}

func (c PurgeClient) LogValue() slog.Value {
	return slog.GroupValue(slog.String("zoneID", c.ZoneID), slog.String("token", "REDACTED"))
}

type purgeRequest struct {
	Hosts []string `json:"hosts"`
}

type purgeResponse struct {
	Success bool `json:"success"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
}

func (c *PurgeClient) PurgeHosts(ctx context.Context, hosts []string) error {
	if len(hosts) == 0 {
		return nil
	}
	if c.ZoneID == "" || c.Token == "" {
		return fmt.Errorf("cloudflare purge: zone id and api token are both required")
	}
	body, err := json.Marshal(purgeRequest{Hosts: hosts})
	if err != nil {
		return fmt.Errorf("cloudflare purge: encode: %w", err)
	}

	base := c.BaseURL
	if base == "" {
		base = defaultBaseURL
	}
	reqCtx, cancel := context.WithTimeout(ctx, purgeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost,
		base+"/zones/"+c.ZoneID+"/purge_cache", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("cloudflare purge: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")

	httpc := c.HTTP
	if httpc == nil {
		httpc = http.DefaultClient
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return fmt.Errorf("cloudflare purge %s: %w", strings.Join(hosts, ","), err)
	}
	defer resp.Body.Close()

	var out purgeResponse
	decErr := json.NewDecoder(io.LimitReader(resp.Body, maxResponse)).Decode(&out)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("cloudflare purge %s: http %d: %s",
			strings.Join(hosts, ","), resp.StatusCode, joinAPIErrors(out))
	}
	if decErr != nil {
		return fmt.Errorf("cloudflare purge %s: decode response: %w", strings.Join(hosts, ","), decErr)
	}
	if !out.Success {
		return fmt.Errorf("cloudflare purge %s: api reported failure: %s",
			strings.Join(hosts, ","), joinAPIErrors(out))
	}
	return nil
}

func joinAPIErrors(r purgeResponse) string {
	if len(r.Errors) == 0 {
		return "no error detail returned"
	}
	parts := make([]string, 0, len(r.Errors))
	for _, e := range r.Errors {
		parts = append(parts, fmt.Sprintf("%d %s", e.Code, e.Message))
	}
	return strings.Join(parts, "; ")
}
