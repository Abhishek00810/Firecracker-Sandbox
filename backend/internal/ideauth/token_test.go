package ideauth

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestHandoffCanOnlyBeRedeemedOnce(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	signer, err := NewSigner(DeriveSigningSecret("worker-secret"))
	if err != nil {
		t.Fatal(err)
	}
	signer.now = func() time.Time { return now }
	nonces := NewMemoryNonceStore()
	nonces.now = func() time.Time { return now }

	handoff, claims, err := signer.IssueHandoff("sandbox-1", "user-1", "org-1", "owner", 3001, DefaultHandoffTTL)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Audience != Audience || claims.Kind != KindHandoff || claims.Nonce == "" {
		t.Fatalf("handoff claims=%+v", claims)
	}

	session, sessionClaims, err := signer.Redeem(context.Background(), handoff, "sandbox-1", 3001, nonces)
	if err != nil {
		t.Fatal(err)
	}
	if sessionClaims.Kind != KindSession || sessionClaims.Nonce != "" {
		t.Fatalf("session claims=%+v", sessionClaims)
	}
	verified, err := signer.VerifySession(session, "sandbox-1", 3001)
	if err != nil || verified.UserID != "user-1" || verified.OrganizationID != "org-1" {
		t.Fatalf("verified=%+v err=%v", verified, err)
	}
	if _, _, err := signer.Redeem(context.Background(), handoff, "sandbox-1", 3001, nonces); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("replayed handoff error=%v", err)
	}
}

func TestTokensAreBoundToKindSandboxAndPort(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	signer, _ := NewSigner(DeriveSigningSecret("worker-secret"))
	signer.now = func() time.Time { return now }
	nonces := NewMemoryNonceStore()
	nonces.now = func() time.Time { return now }
	handoff, _, err := signer.IssueHandoff("sandbox-1", "user-1", "", "owner", 3001, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := signer.VerifySession(handoff, "sandbox-1", 3001); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("handoff accepted as session: %v", err)
	}
	if _, _, err := signer.Redeem(context.Background(), handoff, "sandbox-2", 3001, nonces); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("cross-sandbox redemption error=%v", err)
	}
	if _, _, err := signer.Redeem(context.Background(), handoff, "sandbox-1", 3002, nonces); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("cross-port redemption error=%v", err)
	}
}

func TestExpiredHandoffIsRejected(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	signer, _ := NewSigner(DeriveSigningSecret("worker-secret"))
	signer.now = func() time.Time { return now }
	token, _, err := signer.IssueHandoff("sandbox-1", "user-1", "", "owner", 3001, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	signer.now = func() time.Time { return now.Add(2 * time.Second) }
	if _, _, err := signer.Redeem(context.Background(), token, "sandbox-1", 3001, NewMemoryNonceStore()); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expired handoff error=%v", err)
	}
}

func TestRenewalDoesNotExtendAbsoluteLifetime(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	signer, _ := NewSigner(DeriveSigningSecret("worker-secret"))
	signer.now = func() time.Time { return now }
	nonces := NewMemoryNonceStore()
	nonces.now = func() time.Time { return now }
	handoff, _, _ := signer.IssueHandoff("sandbox-1", "user-1", "", "owner", 3001, time.Minute)
	session, claims, err := signer.Redeem(context.Background(), handoff, "sandbox-1", 3001, nonces)
	if err != nil {
		t.Fatal(err)
	}
	claims.ExpiresAt = claims.AbsoluteExpiry
	session, err = signer.sign(claims)
	if err != nil {
		t.Fatal(err)
	}

	signer.now = func() time.Time { return now.Add(MaximumSessionLife - 30*time.Minute) }
	_, renewed, err := signer.RenewSession(session, "sandbox-1", 3001)
	if err != nil {
		t.Fatal(err)
	}
	if renewed.ExpiresAt != claims.AbsoluteExpiry {
		t.Fatalf("renewed expiry=%d absolute expiry=%d", renewed.ExpiresAt, claims.AbsoluteExpiry)
	}
}
