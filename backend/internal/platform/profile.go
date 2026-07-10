package platform

import (
	"context"
	"fmt"
)

type Profile struct {
	Tier             string
	FreeUSDRemaining float64
}

func (c *Client) GetProfile(userID string) (Profile, error) {
	var profile Profile
	err := c.pool.QueryRow(context.Background(), `
		SELECT tier, free_usd_remaining::double precision
		FROM profiles WHERE id = $1`, userID).Scan(
		&profile.Tier, &profile.FreeUSDRemaining,
	)
	if err != nil {
		return Profile{}, fmt.Errorf("get profile: %w", err)
	}
	return profile, nil
}
