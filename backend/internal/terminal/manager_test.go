package terminal

import (
	"errors"
	"testing"
	"time"
)

const testSecret = "0123456789abcdef0123456789abcdef"

func TestCreateSupportsMultipleTerminalsPerSandbox(t *testing.T) {
	manager, err := NewManager(testSecret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	first, err := manager.Reserve("sandbox-1", "user-1", "worker-1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Reserve("sandbox-1", "user-1", "worker-1")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID {
		t.Fatal("separate terminal sessions received the same id")
	}
	if first.State != StateCreating || second.State != StateCreating {
		t.Fatalf("unexpected states: %q and %q", first.State, second.State)
	}
}

func TestAttachmentTokenIsSingleUse(t *testing.T) {
	manager, err := NewManager(testSecret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	session, err := manager.Reserve("sandbox-1", "user-1", "worker-1")
	if err != nil {
		t.Fatal(err)
	}
	session, token, err := manager.Authorize(session.ID)
	if err != nil {
		t.Fatal(err)
	}

	consumed, err := manager.Consume(token)
	if err != nil {
		t.Fatalf("first consume: %v", err)
	}
	if consumed.ID != session.ID || consumed.SandboxID != "sandbox-1" {
		t.Fatalf("unexpected consumed session: %+v", consumed)
	}
	if _, err := manager.Consume(token); !errors.Is(err, ErrTokenUsed) {
		t.Fatalf("second consume error=%v, want ErrTokenUsed", err)
	}
}

func TestAttachmentTokenRejectsTamperingAndExpiry(t *testing.T) {
	manager, err := NewManager(testSecret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }
	session, err := manager.Reserve("sandbox-1", "user-1", "worker-1")
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := manager.Authorize(session.ID)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := manager.Consume(token + "x"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("tampered token error=%v, want ErrInvalidToken", err)
	}
	manager.now = func() time.Time { return now.Add(time.Minute) }
	if _, err := manager.Consume(token); !errors.Is(err, ErrExpiredToken) {
		t.Fatalf("expired token error=%v, want ErrExpiredToken", err)
	}
}

func TestManagerRequiresStrongSecret(t *testing.T) {
	if _, err := NewManager("too-short", time.Minute); err == nil {
		t.Fatal("NewManager accepted a short token secret")
	}
}

func TestDeriveSigningSecret(t *testing.T) {
	first := DeriveSigningSecret("worker-secret")
	if first != DeriveSigningSecret("worker-secret") {
		t.Fatal("derived signing secret is not deterministic")
	}
	if first == DeriveSigningSecret("different-worker-secret") {
		t.Fatal("different worker tokens derived the same signing secret")
	}
	if len(first) < 32 || first == "worker-secret" {
		t.Fatal("terminal signing secret was not domain-separated")
	}
}

func TestCreateRequiresRoutingIdentity(t *testing.T) {
	manager, err := NewManager(testSecret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Reserve("sandbox-1", "user-1", ""); err == nil {
		t.Fatal("Reserve accepted an empty worker id")
	}
}
