package exec

import (
	"encoding/json"
	"fmt"

	"github.com/vektah/gqlparser/v2/ast"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/jwtmint"
	"github.com/suprbdev/pdbq/internal/schema"
)

// MintOptions turns function results of one composite type into signed JWTs
// (PostGraphile's pgJwtType). A function returning schema.type yields an
// HS256 token string whose claims are the composite's fields; an exp field
// becomes the token expiry.
type MintOptions = jwtmint.Options

// mintField rewrites the raw JSON of a function root field, replacing every
// claims object with its signed token. Volatile functions carry the claims
// under the payload's result field (aliased any number of times); stable
// ones return them directly.
func (e *Executor) mintField(raw json.RawMessage, op *Operation, f *ast.Field, meta *schema.FieldMeta) (json.RawMessage, error) {
	if meta.Function.Volatility != introspect.VolatilityVolatile {
		return jwtmint.Mint(e.Opts.Mint, raw, meta.Function.ReturnsSet)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		return raw, fmt.Errorf("mint: payload: %w", err)
	}
	var payloadMeta map[string]*schema.FieldMeta
	if f.Definition != nil {
		payloadMeta = e.Built.Meta[f.Definition.Type.Name()]
	}
	for _, sub := range collectRootFields(f.SelectionSet, op.Document.Fragments, op.Vars) {
		if m := payloadMeta[sub.Name]; m == nil || m.Kind != schema.KindPayloadResult {
			continue
		}
		v, ok := payload[sub.Alias]
		if !ok {
			continue
		}
		minted, err := jwtmint.Mint(e.Opts.Mint, v, meta.Function.ReturnsSet)
		if err != nil {
			return raw, err
		}
		payload[sub.Alias] = minted
	}
	return json.Marshal(payload)
}
