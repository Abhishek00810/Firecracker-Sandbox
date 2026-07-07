package middleware

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// jwtVerifier validates Supabase user JWTs (ES256, asymmetric) using the project's public
// JWKS. No shared secret is held — only the public key is fetched and cached. Used so the
// dashboard can authenticate as the logged-in user without an API key.
type jwtVerifier struct {
	jwksURL  string
	iss      string // expected issuer: <supabaseURL>/auth/v1
	hsSecret string // legacy HS256 shared secret (empty = HS256 disabled)
	client   *http.Client

	mu      sync.RWMutex
	keys    map[string]*ecdsa.PublicKey // kid -> public key
	fetched time.Time
}

func newJWTVerifier(supabaseURL, hsSecret string) *jwtVerifier {
	base := strings.TrimRight(supabaseURL, "/")
	return &jwtVerifier{
		jwksURL:  base + "/auth/v1/.well-known/jwks.json",
		iss:      base + "/auth/v1",
		hsSecret: hsSecret,
		client:   &http.Client{Timeout: 5 * time.Second},
		keys:     map[string]*ecdsa.PublicKey{},
	}
}

// looksLikeJWT distinguishes a JWT (header.payload.signature) from an API key (no dots).
func looksLikeJWT(token string) bool {
	return strings.Count(token, ".") == 2
}

func (v *jwtVerifier) publicKey(kid string) (*ecdsa.PublicKey, error) {
	v.mu.RLock()
	k, ok := v.keys[kid]
	fresh := time.Since(v.fetched) < time.Hour
	v.mu.RUnlock()
	if ok && fresh {
		return k, nil
	}
	if err := v.refresh(); err != nil {
		if ok { // keep serving with a stale-but-known key if the refresh failed
			return k, nil
		}
		return nil, err
	}
	v.mu.RLock()
	k, ok = v.keys[kid]
	v.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown key id %q", kid)
	}
	return k, nil
}

func (v *jwtVerifier) refresh() error {
	resp, err := v.client.Get(v.jwksURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var set struct {
		Keys []struct {
			Kid, Kty, Crv, X, Y string
		} `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&set); err != nil {
		return err
	}
	keys := map[string]*ecdsa.PublicKey{}
	for _, jk := range set.Keys {
		if jk.Kty != "EC" || jk.Crv != "P-256" {
			continue
		}
		x, err1 := b64uBig(jk.X)
		y, err2 := b64uBig(jk.Y)
		if err1 != nil || err2 != nil {
			continue
		}
		keys[jk.Kid] = &ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}
	}
	if len(keys) == 0 {
		return fmt.Errorf("no usable EC keys in JWKS")
	}
	v.mu.Lock()
	v.keys = keys
	v.fetched = time.Now()
	v.mu.Unlock()
	return nil
}

// verify checks a Supabase ES256 JWT end-to-end and returns the subject (user id).
func (v *jwtVerifier) verify(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("malformed token")
	}
	var hdr struct{ Alg, Kid, Typ string }
	hb, err := b64u(parts[0])
	if err != nil {
		return "", err
	}
	if err := json.Unmarshal(hb, &hdr); err != nil {
		return "", err
	}
	sig, err := b64u(parts[2])
	if err != nil {
		return "", fmt.Errorf("bad signature")
	}
	signingInput := parts[0] + "." + parts[1]
	// Supabase is mid-migration between HS256 (legacy shared secret) and ES256 (JWKS).
	// Accept either; still reject "none" / anything else (alg-confusion protection).
	switch hdr.Alg {
	case "HS256":
		if v.hsSecret == "" {
			return "", fmt.Errorf("HS256 token but no JWT secret configured")
		}
		mac := hmac.New(sha256.New, []byte(v.hsSecret))
		mac.Write([]byte(signingInput))
		if !hmac.Equal(sig, mac.Sum(nil)) {
			return "", fmt.Errorf("signature verification failed")
		}
	case "ES256":
		if len(sig) != 64 { // P-256: r||s, 32 bytes each
			return "", fmt.Errorf("bad signature")
		}
		pub, err := v.publicKey(hdr.Kid)
		if err != nil {
			return "", err
		}
		digest := sha256.Sum256([]byte(signingInput))
		r := new(big.Int).SetBytes(sig[:32])
		s := new(big.Int).SetBytes(sig[32:])
		if !ecdsa.Verify(pub, digest[:], r, s) {
			return "", fmt.Errorf("signature verification failed")
		}
	default:
		return "", fmt.Errorf("unexpected alg %q", hdr.Alg)
	}
	pb, err := b64u(parts[1])
	if err != nil {
		return "", err
	}
	var claims struct {
		Sub string `json:"sub"`
		Exp int64  `json:"exp"`
		Iss string `json:"iss"`
		Aud any    `json:"aud"`
	}
	if err := json.Unmarshal(pb, &claims); err != nil {
		return "", err
	}
	if claims.Sub == "" {
		return "", fmt.Errorf("no subject")
	}
	if claims.Exp != 0 && time.Now().After(time.Unix(claims.Exp, 0)) {
		return "", fmt.Errorf("token expired")
	}
	if v.iss != "" && claims.Iss != v.iss {
		return "", fmt.Errorf("bad issuer")
	}
	if !audContains(claims.Aud, "authenticated") {
		return "", fmt.Errorf("bad audience")
	}
	return claims.Sub, nil
}

func audContains(aud any, want string) bool {
	switch a := aud.(type) {
	case string:
		return a == want
	case []any:
		for _, x := range a {
			if s, ok := x.(string); ok && s == want {
				return true
			}
		}
	}
	return false
}

func b64u(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }

func b64uBig(s string) (*big.Int, error) {
	b, err := b64u(s)
	if err != nil {
		return nil, err
	}
	return new(big.Int).SetBytes(b), nil
}
