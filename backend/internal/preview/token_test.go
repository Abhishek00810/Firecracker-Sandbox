package preview

import (
	"errors"
	"testing"
	"time"
)

func TestSignerBindsTokenToSandboxAndPort(t *testing.T) {
	signer, err := NewSigner(DeriveSigningSecret("worker-secret-worker-secret-worker-secret"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	signer.now = func() time.Time { return now }
	token, expiresAt, err := signer.Sign("sandbox-1", "user-1", 3000, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !expiresAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("expires_at=%s", expiresAt)
	}
	claims, err := signer.Verify(token, "sandbox-1", 3000)
	if err != nil {
		t.Fatal(err)
	}
	if claims.UserID != "user-1" {
		t.Fatalf("user_id=%q", claims.UserID)
	}
	if _, err := signer.Verify(token, "sandbox-2", 3000); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("cross-sandbox verify error=%v", err)
	}
	if _, err := signer.Verify(token, "sandbox-1", 5173); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("cross-port verify error=%v", err)
	}
}

func TestSignerRejectsExpiredAndTamperedTokens(t *testing.T) {
	signer, _ := NewSigner(DeriveSigningSecret("worker-secret-worker-secret-worker-secret"))
	now := time.Unix(1_800_000_000, 0)
	signer.now = func() time.Time { return now }
	token, _, err := signer.Sign("sandbox-1", "user-1", 3000, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	signer.now = func() time.Time { return now.Add(2 * time.Second) }
	if _, err := signer.Verify(token, "sandbox-1", 3000); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expired verify error=%v", err)
	}
	if _, err := signer.Verify(token+"x", "sandbox-1", 3000); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("tampered verify error=%v", err)
	}
}
