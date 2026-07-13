package exec

import (
	"encoding/json"
	"fmt"

	"github.com/golang-jwt/jwt/v5"
	"github.com/vektah/gqlparser/v2/ast"

	"github.com/suprbdev/pdbq/internal/introspect"
	"github.com/suprbdev/pdbq/internal/schema"
)

// MintOptions turns function results of one composite type into signed JWTs
// (PostGraphile's pgJwtType). A function returning schema.type yields an
// HS256 token string whose claims are the composite's fields; an exp field
// becomes the token expiry.
type MintOptions struct {
	Schema string // pg schema of the composite, e.g. "public"
	Type   string // composite type name, e.g. "jwt"
	Secret string
	Issuer   string
	Audience string
}

func (m MintOptions) Enabled() bool { return m.Type != "" }

// mintsFunction reports whether f's return type is the mint composite.
func (m MintOptions) mintsFunction(f *introspect.Function) bool {
	return m.Enabled() && f != nil && f.ReturnTypeSchema == m.Schema && f.ReturnType == m.Type
}

// mintField rewrites the raw JSON of a function root field, replacing every
// claims object with its signed token. Volatile functions carry the claims
// under the payload's result field(s); stable ones return them directly.
func (e *Executor) mintField(raw json.RawMessage, op *Operation, f *ast.Field, meta *schema.FieldMeta) (json.RawMessage, error) {
	if meta.Function.Volatility != introspect.VolatilityVolatile {
		return e.mintValue(raw, meta.Function.ReturnsSet)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		return raw, fmt.Errorf("mint: payload: %w", err)
	}
	for _, sub := range collectRootFields(f.SelectionSet, op.Document.Fragments) {
		if sub.Name != "result" {
			continue
		}
		v, ok := payload[sub.Alias]
		if !ok {
			continue
		}
		minted, err := e.mintValue(v, meta.Function.ReturnsSet)
		if err != nil {
			return raw, err
		}
		payload[sub.Alias] = minted
	}
	return json.Marshal(payload)
}

// mintValue signs one claims object (or each element of a set-returning
// function's array). JSON null stays null.
func (e *Executor) mintValue(raw json.RawMessage, set bool) (json.RawMessage, error) {
	if set {
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return raw, fmt.Errorf("mint: set: %w", err)
		}
		for i, item := range items {
			minted, err := e.mintValue(item, false)
			if err != nil {
				return raw, err
			}
			items[i] = minted
		}
		return json.Marshal(items)
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		return raw, fmt.Errorf("mint: claims: %w", err)
	}
	if claims == nil {
		return json.RawMessage("null"), nil
	}
	token, err := e.signClaims(claims)
	if err != nil {
		return raw, fmt.Errorf("mint: sign: %w", err)
	}
	return json.Marshal(token)
}

func (e *Executor) signClaims(claims map[string]any) (string, error) {
	mc := jwt.MapClaims{}
	for k, v := range claims {
		if v != nil {
			mc[k] = v
		}
	}
	if iss := e.Opts.Mint.Issuer; iss != "" {
		mc["iss"] = iss
	}
	if aud := e.Opts.Mint.Audience; aud != "" {
		mc["aud"] = aud
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, mc).SignedString([]byte(e.Opts.Mint.Secret))
}
