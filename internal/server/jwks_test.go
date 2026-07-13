package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/suprbdev/pdbq/internal/config"
)

func rsaJWK(t *testing.T, kid string, pub *rsa.PublicKey) map[string]string {
	t.Helper()
	return map[string]string{
		"kty": "RSA",
		"kid": kid,
		"use": "sig",
		"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}
}

func ecJWK(t *testing.T, kid string, pub *ecdsa.PublicKey) map[string]string {
	t.Helper()
	size := (pub.Curve.Params().BitSize + 7) / 8
	return map[string]string{
		"kty": "EC",
		"kid": kid,
		"use": "sig",
		"crv": "P-256",
		"x":   base64.RawURLEncoding.EncodeToString(pub.X.FillBytes(make([]byte, size))),
		"y":   base64.RawURLEncoding.EncodeToString(pub.Y.FillBytes(make([]byte, size))),
	}
}

// jwksServer serves the given keys and counts fetches.
func jwksServer(t *testing.T, keys *[]map[string]string, fetches *int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*fetches++
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": *keys})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func jwksAuth(url string) *Authenticator {
	return NewAuthenticator(config.RLS{
		Enabled:       true,
		AnonymousRole: "anonymous",
		RoleClaim:     "role",
		Auth:          config.Auth{Mode: "jwt", JWKSURL: url, JWKSCacheTTL: time.Hour},
	})
}

func bearerRequest(token string) *http.Request {
	r := httptest.NewRequest("POST", "/graphql", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	return r
}

func TestJWKSRS256(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keys := []map[string]string{rsaJWK(t, "k1", &key.PublicKey)}
	fetches := 0
	srv := jwksServer(t, &keys, &fetches)
	a := jwksAuth(srv.URL)

	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"role": "app_user",
		"exp":  time.Now().Add(time.Hour).Unix(),
	})
	tok.Header["kid"] = "k1"
	signed, err := tok.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	_, role, err := a.Authenticate(bearerRequest(signed))
	if err != nil {
		t.Fatalf("RS256 token rejected: %v", err)
	}
	if role != "app_user" {
		t.Fatalf("role = %q, want app_user", role)
	}
	// Second verification hits the cache.
	if _, _, err := a.Authenticate(bearerRequest(signed)); err != nil {
		t.Fatal(err)
	}
	if fetches != 1 {
		t.Fatalf("JWKS fetched %d times, want 1 (cache)", fetches)
	}
}

func TestJWKSES256(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keys := []map[string]string{ecJWK(t, "e1", &key.PublicKey)}
	fetches := 0
	srv := jwksServer(t, &keys, &fetches)
	a := jwksAuth(srv.URL)

	tok := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"role": "app_user",
		"exp":  time.Now().Add(time.Hour).Unix(),
	})
	tok.Header["kid"] = "e1"
	signed, err := tok.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.Authenticate(bearerRequest(signed)); err != nil {
		t.Fatalf("ES256 token rejected: %v", err)
	}
}

func TestJWKSRejectsWrongKeyAndHMAC(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	keys := []map[string]string{rsaJWK(t, "k1", &key.PublicKey)}
	fetches := 0
	srv := jwksServer(t, &keys, &fetches)
	a := jwksAuth(srv.URL)

	// Signed by a different key.
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"role": "app_user", "exp": time.Now().Add(time.Hour).Unix(),
	})
	tok.Header["kid"] = "k1"
	signed, _ := tok.SignedString(other)
	if _, _, err := a.Authenticate(bearerRequest(signed)); err == nil {
		t.Fatal("token signed by wrong key accepted")
	}

	// HS256 must be off the algorithm allowlist in JWKS mode (no downgrade
	// to HMAC-with-public-key).
	hmacTok, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"role": "app_user", "exp": time.Now().Add(time.Hour).Unix(),
	}).SignedString([]byte("secret"))
	if _, _, err := a.Authenticate(bearerRequest(hmacTok)); err == nil {
		t.Fatal("HS256 token accepted in JWKS mode")
	}
}

func TestJWKSRotationRefetch(t *testing.T) {
	key1, _ := rsa.GenerateKey(rand.Reader, 2048)
	key2, _ := rsa.GenerateKey(rand.Reader, 2048)
	keys := []map[string]string{rsaJWK(t, "k1", &key1.PublicKey)}
	fetches := 0
	srv := jwksServer(t, &keys, &fetches)
	a := jwksAuth(srv.URL)

	sign := func(key *rsa.PrivateKey, kid string) string {
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
			"role": "app_user", "exp": time.Now().Add(time.Hour).Unix(),
		})
		tok.Header["kid"] = kid
		s, err := tok.SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	if _, _, err := a.Authenticate(bearerRequest(sign(key1, "k1"))); err != nil {
		t.Fatal(err)
	}
	// Rotate: new kid appears at the endpoint. Force the refetch window open
	// (unknown kids refetch at most once per minute).
	keys = []map[string]string{rsaJWK(t, "k2", &key2.PublicKey)}
	a.jwks.mu.Lock()
	a.jwks.fetched = time.Now().Add(-2 * time.Minute)
	a.jwks.mu.Unlock()
	if _, _, err := a.Authenticate(bearerRequest(sign(key2, "k2"))); err != nil {
		t.Fatalf("rotated key rejected: %v", err)
	}
	if fetches != 2 {
		t.Fatalf("JWKS fetched %d times, want 2 (initial + rotation)", fetches)
	}
}
