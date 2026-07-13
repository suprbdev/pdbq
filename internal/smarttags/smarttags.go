// Package smarttags parses PostGraphile-style "smart comments": lines at the
// start of a database COMMENT beginning with '@' are machine-readable tags,
// everything after the first non-tag line is the human description.
//
//	COMMENT ON TABLE users IS E'@name people\n@omit delete\nA user of the app.';
//
// parses to {name: ["people"], omit: ["delete"]} plus "A user of the app.".
// The parser is shared by the smart-comments and advanced-filters plugins so
// both read the same syntax from the same catalog comments.
package smarttags

import "strings"

// Tags maps a tag name to its values, one entry per occurrence in the
// comment; a bare tag (no value) contributes an empty string. A nil Tags is
// valid and empty.
type Tags map[string][]string

// Parse splits a comment into its smart tags and the remaining description.
// Comments with no leading '@' line are returned unchanged with nil tags.
func Parse(comment string) (Tags, string) {
	if !strings.HasPrefix(strings.TrimSpace(comment), "@") {
		return nil, comment
	}
	lines := strings.Split(comment, "\n")
	tags := Tags{}
	i := 0
	for ; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue // blank lines between tags don't end the tag block
		}
		if !strings.HasPrefix(line, "@") {
			break
		}
		name, value := line[1:], ""
		if cut := strings.IndexAny(name, " \t"); cut >= 0 {
			name, value = name[:cut], strings.TrimSpace(name[cut+1:])
		}
		if name != "" {
			tags[name] = append(tags[name], value)
		}
	}
	return tags, strings.TrimSpace(strings.Join(lines[i:], "\n"))
}

// Strip returns the comment with any leading smart-tag lines removed.
func Strip(comment string) string {
	_, desc := Parse(comment)
	return desc
}

// Has reports whether the tag occurs at least once.
func (t Tags) Has(name string) bool {
	_, ok := t[name]
	return ok
}

// First returns the first non-empty value of the tag, or "".
func (t Tags) First(name string) string {
	for _, v := range t[name] {
		if v != "" {
			return v
		}
	}
	return ""
}

// All returns every value of the tag in order (nil when absent).
func (t Tags) All(name string) []string { return t[name] }

// Omits collects the actions named across all @omit occurrences (lowercased,
// split on commas and whitespace: "@omit create, update"). A bare @omit sets
// everything instead: the object is omitted entirely.
//
// PostGraphile-V5-style @behavior tags contribute too: their negative tokens
// map onto omit actions (-insert -> create, -update -> update, -delete ->
// delete, -select -> read, -filter -> filter, -order -> order); other tokens
// are ignored.
func (t Tags) Omits() (actions map[string]bool, everything bool) {
	actions = map[string]bool{}
	for _, v := range t["omit"] {
		if v == "" {
			everything = true
			continue
		}
		for _, a := range SplitList(v) {
			actions[strings.ToLower(a)] = true
		}
	}
	for _, v := range t["behavior"] {
		for _, tok := range SplitList(v) {
			if action, ok := behaviorOmits[strings.ToLower(tok)]; ok {
				actions[action] = true
			}
		}
	}
	return actions, everything
}

// behaviorOmits maps negative @behavior tokens to @omit actions.
var behaviorOmits = map[string]string{
	"-insert": "create",
	"-update": "update",
	"-delete": "delete",
	"-select": "read",
	"-filter": "filter",
	"-order":  "order",
}

// SplitList splits a tag value on commas and whitespace, dropping empties —
// the list form shared by @omit, @primaryKey, and @unique values.
func SplitList(v string) []string {
	return strings.FieldsFunc(v, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t'
	})
}
