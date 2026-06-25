package gateway

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestFlexStrings_UnmarshalShapes(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{`["a","b"]`, []string{"a", "b"}},
		{`"foo"`, []string{"foo"}},
		{`"a, b ,c"`, []string{"a", "b", "c"}}, // comma-split + trim
		{`" x "`, []string{"x"}},
		{`""`, nil},
		{`null`, nil},
		{`[]`, nil},
		{`["a","","  ",  "b"]`, []string{"a", "b"}}, // drops empties
	}
	for _, c := range cases {
		var f flexStrings
		if err := json.Unmarshal([]byte(c.in), &f); err != nil {
			t.Fatalf("%s: unmarshal err %v", c.in, err)
		}
		if !reflect.DeepEqual([]string(f), c.want) {
			t.Fatalf("%s: got %v, want %v", c.in, []string(f), c.want)
		}
	}
}

// TestFlexStrings_InStruct proves the papercut fix: a `tags` field that used
// to reject a bare string now accepts both string and array forms.
func TestFlexStrings_InStruct(t *testing.T) {
	var s struct {
		Tags flexStrings `json:"tags"`
	}
	if err := json.Unmarshal([]byte(`{"tags":"solo"}`), &s); err != nil {
		t.Fatalf("string tags: %v", err)
	}
	if len(s.Tags) != 1 || s.Tags[0] != "solo" {
		t.Fatalf("string tags = %v", s.Tags)
	}
	if err := json.Unmarshal([]byte(`{"tags":["a","b"]}`), &s); err != nil {
		t.Fatalf("array tags: %v", err)
	}
	if len(s.Tags) != 2 {
		t.Fatalf("array tags = %v", s.Tags)
	}
}
