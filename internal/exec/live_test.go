package exec

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestPlanRelations(t *testing.T) {
	plan := `[{"Plan": {"Node Type": "Aggregate", "Plans": [
		{"Node Type": "Seq Scan", "Relation Name": "posts", "Schema": "public", "Alias": "p"},
		{"Node Type": "Index Scan", "Parent Relationship": "SubPlan", "Relation Name": "users", "Schema": "app", "Alias": "u"},
		{"Node Type": "Append", "Plans": [
			{"Node Type": "Seq Scan", "Relation Name": "events_2026", "Schema": "public"},
			{"Node Type": "Function Scan", "Function Name": "search_posts", "Schema": "public"}
		]},
		{"Node Type": "Seq Scan", "Relation Name": "nameless"}
	]}}]`
	var doc any
	if err := json.Unmarshal([]byte(plan), &doc); err != nil {
		t.Fatal(err)
	}
	var got []string
	planRelations(doc, func(nsp, rel string) { got = append(got, nsp+"."+rel) })
	slices.Sort(got)
	if want := []string{"app.users", "public.events_2026", "public.posts"}; !slices.Equal(got, want) {
		t.Fatalf("relations = %q, want %q", got, want)
	}
}
