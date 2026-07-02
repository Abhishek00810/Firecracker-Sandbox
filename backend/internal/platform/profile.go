package platform

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// Profile is the tier + balance for a user, looked up when a request authenticates via a
// Supabase JWT (dashboard) rather than an API key.
type Profile struct {
	Tier             string
	FreeUSDRemaining float64
}

// GetProfile fetches a user's tier + remaining balance from public.profiles (service role).
func (c *Client) GetProfile(userID string) (Profile, error) {
	url := c.baseURL + "/rest/v1/profiles?id=eq." + userID + "&select=tier,free_usd_remaining"
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return Profile{}, err
	}
	req.Header.Set("apikey", c.serviceRoleKey)
	req.Header.Set("Authorization", "Bearer "+c.serviceRoleKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return Profile{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return Profile{}, fmt.Errorf("profile lookup status %d", resp.StatusCode)
	}

	var rows []struct {
		Tier             string  `json:"tier"`
		FreeUSDRemaining float64 `json:"free_usd_remaining"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return Profile{}, err
	}
	if len(rows) == 0 {
		return Profile{}, fmt.Errorf("profile not found for user %s", userID)
	}
	return Profile{Tier: rows[0].Tier, FreeUSDRemaining: rows[0].FreeUSDRemaining}, nil
}
