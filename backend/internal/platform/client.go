package platform

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// KeyRecord is the resolved result of a Supabase api_keys lookup.
type KeyRecord struct {
	ID               string  `json:"id"`
	UserID           string  `json:"user_id"`
	Tier             string  `json:"tier"`
	IsActive         bool    `json:"is_active"`
	ExpiresAt        *string `json:"expires_at"`
	FreeUSDRemaining float64 `json:"free_usd_remaining"`
}

// Client talks to Supabase via the REST API using the service role key.
// The service role key bypasses RLS — only use server-side, never expose to clients.
type Client struct {
	baseURL        string
	serviceRoleKey string
	http           *http.Client
}

func NewClient(supabaseURL, serviceRoleKey string) *Client {
	return &Client{
		baseURL:        supabaseURL,
		serviceRoleKey: serviceRoleKey,
		http:           &http.Client{Timeout: 10 * time.Second},
	}
}

// ResolveKey looks up an api_keys row by its SHA-256 hash and joins profiles
// to get the user's remaining free balance. Returns an error if not found.
func (c *Client) ResolveKey(keyHash string) (KeyRecord, error) {
	// Build URL using url.Values so all query params are properly encoded —
	// prevents query parameter injection even though SHA-256 output is always safe hex.
	base, err := url.Parse(c.baseURL + "/rest/v1/api_keys")
	if err != nil {
		return KeyRecord{}, fmt.Errorf("parse base url: %w", err)
	}

	q := url.Values{}
	q.Set("key_hash", "eq."+keyHash)
	q.Set("select", "id,user_id,is_active,expires_at,profiles(tier,free_usd_remaining)")
	q.Set("limit", "1")
	base.RawQuery = q.Encode()

	req, err := http.NewRequest(http.MethodGet, base.String(), nil)
	if err != nil {
		return KeyRecord{}, fmt.Errorf("build request: %w", err)
	}

	req.Header.Set("apikey", c.serviceRoleKey)
	req.Header.Set("Authorization", "Bearer "+c.serviceRoleKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return KeyRecord{}, fmt.Errorf("supabase request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return KeyRecord{}, fmt.Errorf("supabase returned status %d", resp.StatusCode)
	}

	// PostgREST always returns an array
	var rows []struct {
		ID        string  `json:"id"`
		UserID    string  `json:"user_id"`
		IsActive  bool    `json:"is_active"`
		ExpiresAt *string `json:"expires_at"`
		Profiles  struct {
			Tier             string  `json:"tier"`
			FreeUSDRemaining float64 `json:"free_usd_remaining"`
		} `json:"profiles"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return KeyRecord{}, fmt.Errorf("decode response: %w", err)
	}

	if len(rows) == 0 {
		return KeyRecord{}, fmt.Errorf("key not found")
	}

	row := rows[0]
	return KeyRecord{
		ID:               row.ID,
		UserID:           row.UserID,
		Tier:             row.Profiles.Tier,
		IsActive:         row.IsActive,
		ExpiresAt:        row.ExpiresAt,
		FreeUSDRemaining: row.Profiles.FreeUSDRemaining,
	}, nil
}
