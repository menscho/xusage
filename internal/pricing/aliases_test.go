package pricing

import (
	"sort"
	"testing"
)

func TestParseAliasesJSON(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want map[string]string
	}{
		{"empty", "", map[string]string{}},
		{"whitespace only", "   \n ", map[string]string{}},
		{"json object", `{"a":"b","c":"d"}`, map[string]string{"a": "b", "c": "d"}},
		{
			"json skips empty keys and values",
			`{"a":"b","":"x","k":""}`,
			map[string]string{"a": "b"},
		},
		{"invalid json yields empty", "{oops", map[string]string{}},
		{"json non-string field ignored", `{"a":1,"b":"c"}`, map[string]string{"b": "c"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseAliases(tt.raw)
			assertAliasMap(t, got, tt.want)
		})
	}
}

func TestParseAliasesList(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want map[string]string
	}{
		{"single pair", "a=b", map[string]string{"a": "b"}},
		{"comma separated", "a=b,c=d", map[string]string{"a": "b", "c": "d"}},
		{"semicolon separated", "a=b;c=d", map[string]string{"a": "b", "c": "d"}},
		{"newline separated", "a=b\nc=d", map[string]string{"a": "b", "c": "d"}},
		{"spaces trimmed", " a = b , c=d ", map[string]string{"a": "b", "c": "d"}},
		{"missing equals skipped", "a=b,broken,c=d", map[string]string{"a": "b", "c": "d"}},
		{"empty side skipped", "=b,a=", map[string]string{}},
		{"braces tolerated on list form", "{a=b}", map[string]string{"a": "b"}},
		{"value containing equals keeps rest", "a=b=c", map[string]string{"a": "b=c"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseAliases(tt.raw)
			assertAliasMap(t, got, tt.want)
		})
	}
}

// TestParseAliasesJSONBeatsList pins the dispatch: anything starting with '{'
// goes to the JSON parser only.
func TestParseAliasesJSONBeatsList(t *testing.T) {
	got := parseAliases(`{"a":"b"},c=d`)
	assertAliasMap(t, got, map[string]string{"a": "b"})
}

func assertAliasMap(t *testing.T, got, want map[string]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("parseAliases = %v (len %d), want %v (len %d)", got, len(got), want, len(want))
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("parseAliases[%q] = %q, want %q", k, got[k], v)
		}
	}
	keys := make([]string, 0, len(got))
	for k := range got {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic iteration in failure output
}
