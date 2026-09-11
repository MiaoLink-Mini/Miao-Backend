package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/dlclark/regexp2"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"weagent/backend/contracts"
)

type Object = map[string]any
type Validator struct{ schemas map[string]*jsonschema.Schema }
type regex struct{ *regexp2.Regexp }

func (r regex) MatchString(s string) bool {
	ok, err := r.Regexp.MatchString(s)
	return err == nil && ok
}

func New() (*Validator, error) {
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	c.UseRegexpEngine(func(pattern string) (jsonschema.Regexp, error) {
		r, err := regexp2.Compile(pattern, regexp2.ECMAScript)
		if err != nil {
			return nil, err
		}
		r.MatchTimeout = 50 * time.Millisecond
		return regex{r}, nil
	})
	doc, err := Decode(contracts.Schema)
	if err != nil {
		return nil, err
	}
	root := doc.(map[string]any)
	id := root["$id"].(string)
	if err = c.AddResource(id, root); err != nil {
		return nil, err
	}
	v := &Validator{schemas: map[string]*jsonschema.Schema{}}
	for name := range root["$defs"].(map[string]any) {
		s, err := c.Compile(id + "#/$defs/" + name)
		if err != nil {
			return nil, fmt.Errorf("schema %s: %w", name, err)
		}
		v.schemas[name] = s
	}
	return v, nil
}
func (v *Validator) Validate(name string, value any) error {
	s, ok := v.schemas[name]
	if !ok {
		return fmt.Errorf("unknown schema %s", name)
	}
	// Normalize Go structs/int64/time values to the JSON data model before validating.
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	x, err := Decode(b)
	if err != nil {
		return err
	}
	return s.Validate(x)
}

// Decode rejects duplicate properties, trailing documents and excessive nesting.
func Decode(b []byte) (any, error) {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	v, err := readValue(d, 0)
	if err != nil {
		return nil, err
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, errors.New("trailing JSON")
	}
	return v, nil
}
func readValue(d *json.Decoder, depth int) (any, error) {
	if depth > 64 {
		return nil, errors.New("JSON nesting limit")
	}
	t, err := d.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := t.(json.Delim); ok {
		switch delim {
		case '{':
			m := map[string]any{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return nil, err
				}
				k, ok := key.(string)
				if !ok {
					return nil, errors.New("invalid key")
				}
				if _, seen := m[k]; seen {
					return nil, errors.New("duplicate key")
				}
				value, err := readValue(d, depth+1)
				if err != nil {
					return nil, err
				}
				m[k] = value
			}
			_, err = d.Token()
			return m, err
		case '[':
			a := []any{}
			for d.More() {
				value, err := readValue(d, depth+1)
				if err != nil {
					return nil, err
				}
				a = append(a, value)
			}
			_, err = d.Token()
			return a, err
		default:
			return nil, errors.New("unexpected delimiter")
		}
	}
	return t, nil
}
