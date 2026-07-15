package platform

import (
	"context"
	"fmt"
)

type Profile struct {
	BalanceUSD float64
}

func (c *Client) GetProfile(userID string) (Profile, error) {
	var profile Profile
	err := c.pool.QueryRow(context.Background(), `
		SELECT balance_usd::double precision
		FROM profiles WHERE id = $1`, userID).Scan(&profile.BalanceUSD)
	if err != nil {
		return Profile{}, fmt.Errorf("get profile: %w", err)
	}
	return profile, nil
}
