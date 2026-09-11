package protocol

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestCanonicalExamples(t *testing.T) {
	v, e := New()
	if e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile("../../contracts/examples.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixtures struct {
		Cases []struct {
			Name, Schema string
			Valid        bool
			Value        any
		}
	}
	if e = json.Unmarshal(b, &fixtures); e != nil {
		t.Fatal(e)
	}
	for _, f := range fixtures.Cases {
		t.Run(f.Name, func(t *testing.T) {
			e := v.Validate(f.Schema, f.Value)
			if (e == nil) != f.Valid {
				t.Fatalf("valid=%v expected=%v: %v", e == nil, f.Valid, e)
			}
		})
	}
}
func TestStrictJSON(t *testing.T) {
	for _, b := range []string{`{"id":1,"id":2}`, `{} {}`, strings.Repeat("[", 66) + strings.Repeat("]", 66), `[1,]`} {
		if _, e := Decode([]byte(b)); e == nil {
			t.Fatalf("accepted invalid JSON %q", b)
		}
	}
	if _, e := Decode([]byte(`{"x":[1,true,null,{"a":"b"}]}`)); e != nil {
		t.Fatal(e)
	}
}

func TestSequencePrecision(t *testing.T) {
	v, e := New()
	if e != nil {
		t.Fatal(e)
	}
	for _, number := range []string{"9007199254740991.1", "9007199254740992", "-1"} {
		value, e := Decode([]byte(number))
		if e != nil {
			t.Fatal(e)
		}
		if v.Validate("Sequence", value) == nil {
			t.Fatal("unsafe sequence accepted:", number)
		}
	}
	for _, number := range []string{"1e0", "9007199254740991"} {
		value, e := Decode([]byte(number))
		if e != nil || v.Validate("Sequence", value) != nil {
			t.Fatal("safe sequence rejected", number)
		}
	}
}
