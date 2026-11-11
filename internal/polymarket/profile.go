package polymarket

import (
	"context"
	"encoding/json"
	"fmt"
)

type ProfileRaw map[string]any

type ProfileResult struct {
	Raw      *RawResponse
	Parsed   ProfileRaw
	Endpoint string
	Params   map[string]string
}

func (c *Client) GetPublicProfile(ctx context.Context, address string) (*ProfileResult, error) {
	if address == "" {
		return nil, fmt.Errorf("address required")
	}
	params := map[string]string{"address": address}
	full := buildURL(c.gammaBase, "/public-profile", params)
	raw, err := c.getJSON(ctx, "gamma", full)
	if err != nil {
		return nil, err
	}
	var parsed ProfileRaw
	if err := json.Unmarshal(raw.Body, &parsed); err != nil {
		return nil, fmt.Errorf("decode profile: %w", err)
	}
	return &ProfileResult{
		Raw:      raw,
		Parsed:   parsed,
		Endpoint: "/public-profile",
		Params:   params,
	}, nil
}

func ProxyWalletFromProfile(p ProfileRaw) string {
	if p == nil {
		return ""
	}
	if v, ok := p["proxyWallet"].(string); ok {
		return v
	}
	return ""
}

func DisplayNameFromProfile(p ProfileRaw) string {
	if p == nil {
		return ""
	}
	if v, ok := p["name"].(string); ok && v != "" {
		return v
	}
	if v, ok := p["pseudonym"].(string); ok && v != "" {
		return v
	}
	return ""
}

func CreatedAtSourceFromProfile(p ProfileRaw) string {
	if p == nil {
		return ""
	}
	if v, ok := p["createdAt"].(string); ok {
		return v
	}
	return ""
}
