package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"sync"
	"time"
)

// jwk is the subset of RFC 7517 needed to build RSA and EC public keys.
type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

func (k jwk) publicKey() (any, error) {
	switch k.Kty {
	case "RSA":
		n, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			return nil, fmt.Errorf("jwks: key %q: bad modulus: %w", k.Kid, err)
		}
		e, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			return nil, fmt.Errorf("jwks: key %q: bad exponent: %w", k.Kid, err)
		}
		return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}, nil
	case "EC":
		var curve elliptic.Curve
		switch k.Crv {
		case "P-256":
			curve = elliptic.P256()
		case "P-384":
			curve = elliptic.P384()
		case "P-521":
			curve = elliptic.P521()
		default:
			return nil, fmt.Errorf("jwks: key %q: unsupported curve %q", k.Kid, k.Crv)
		}
		x, err := base64.RawURLEncoding.DecodeString(k.X)
		if err != nil {
			return nil, fmt.Errorf("jwks: key %q: bad x: %w", k.Kid, err)
		}
		y, err := base64.RawURLEncoding.DecodeString(k.Y)
		if err != nil {
			return nil, fmt.Errorf("jwks: key %q: bad y: %w", k.Kid, err)
		}
		return &ecdsa.PublicKey{Curve: curve, X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}, nil
	default:
		return nil, fmt.Errorf("jwks: key %q: unsupported key type %q", k.Kid, k.Kty)
	}
}

// jwksCache fetches and caches a JWKS endpoint's signing keys. Keys refresh
// after ttl, and an unknown kid triggers an early refetch (key rotation) at
// most once per minute so a flood of bad tokens cannot hammer the endpoint.
type jwksCache struct {
	url    string
	ttl    time.Duration
	client *http.Client

	mu      sync.Mutex
	keys    map[string]any
	fetched time.Time
}

func newJWKSCache(url string, ttl time.Duration) *jwksCache {
	if ttl <= 0 {
		ttl = time.Hour
	}
	return &jwksCache{url: url, ttl: ttl, client: &http.Client{Timeout: 10 * time.Second}}
}

// key returns the verification key for kid. An empty kid is accepted when
// the set holds exactly one key.
func (c *jwksCache) key(kid string) (any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.keys == nil || time.Since(c.fetched) > c.ttl {
		if err := c.fetchLocked(); err != nil {
			return nil, err
		}
	}
	if k, ok := c.lookupLocked(kid); ok {
		return k, nil
	}
	if time.Since(c.fetched) > time.Minute {
		if err := c.fetchLocked(); err != nil {
			return nil, err
		}
		if k, ok := c.lookupLocked(kid); ok {
			return k, nil
		}
	}
	return nil, fmt.Errorf("jwks: no key for kid %q", kid)
}

func (c *jwksCache) lookupLocked(kid string) (any, bool) {
	if kid != "" {
		k, ok := c.keys[kid]
		return k, ok
	}
	if len(c.keys) == 1 {
		for _, k := range c.keys {
			return k, true
		}
	}
	return nil, false
}

func (c *jwksCache) fetchLocked() error {
	resp, err := c.client.Get(c.url)
	if err != nil {
		return fmt.Errorf("jwks: fetch %s: %w", c.url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("jwks: fetch %s: status %d", c.url, resp.StatusCode)
	}
	var doc struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 1<<20)).Decode(&doc); err != nil {
		return fmt.Errorf("jwks: decode %s: %w", c.url, err)
	}
	keys := map[string]any{}
	for _, k := range doc.Keys {
		if k.Use != "" && k.Use != "sig" {
			continue
		}
		pub, err := k.publicKey()
		if err != nil {
			continue // skip unsupported entries (e.g. symmetric or OKP keys)
		}
		keys[k.Kid] = pub
	}
	if len(keys) == 0 {
		return fmt.Errorf("jwks: %s: no usable signing keys", c.url)
	}
	c.keys = keys
	c.fetched = time.Now()
	return nil
}
