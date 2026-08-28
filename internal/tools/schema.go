// Package tools defines the agent's action surface (step 8) and validates every
// call before it runs (step 12).
//
// Each tool carries a JSON Schema that is used three times: it is sent to the
// model as the tool's input_schema with strict mode on, it is enforced locally
// before Run is entered, and it is what the docs are generated from. One schema,
// three consumers - so a tool cannot drift from its own contract.
package tools

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Schema is a deliberately small JSON Schema subset: object, string, integer,
// number, boolean, array, with required / enum / additionalProperties / bounds.
// A larger subset would be a dependency; this covers every tool here and fails
// loudly on anything it does not understand rather than passing it through.
type Schema struct {
	Type                 string             `json:"type,omitempty"`
	Description          string             `json:"description,omitempty"`
	Properties           map[string]*Schema `json:"properties,omitempty"`
	Required             []string           `json:"required,omitempty"`
	AdditionalProperties *bool              `json:"additionalProperties,omitempty"`
	Items                *Schema            `json:"items,omitempty"`
	Enum                 []string           `json:"enum,omitempty"`
	MinLength            *int               `json:"minLength,omitempty"`
	MaxLength            *int               `json:"maxLength,omitempty"`
	Minimum              *float64           `json:"minimum,omitempty"`
	Maximum              *float64           `json:"maximum,omitempty"`
	MinItems             *int               `json:"minItems,omitempty"`
	MaxItems             *int               `json:"maxItems,omitempty"`
	Default              any                `json:"default,omitempty"`
}

func boolPtr(b bool) *bool    { return &b }
func intPtr(i int) *int       { return &i }
func fPtr(f float64) *float64 { return &f }

// Obj is the constructor used by every tool definition below.
func Obj(desc string, props map[string]*Schema, required ...string) *Schema {
	sort.Strings(required)
	return &Schema{
		Type: "object", Description: desc, Properties: props,
		Required: required, AdditionalProperties: boolPtr(false),
	}
}

func Str(desc string, enum ...string) *Schema {
	return &Schema{Type: "string", Description: desc, Enum: enum}
}

func StrMin(desc string, min int) *Schema {
	return &Schema{Type: "string", Description: desc, MinLength: intPtr(min)}
}

func Int(desc string, min, max float64) *Schema {
	return &Schema{Type: "integer", Description: desc, Minimum: fPtr(min), Maximum: fPtr(max)}
}

func Bool(desc string) *Schema { return &Schema{Type: "boolean", Description: desc} }

func Arr(desc string, item *Schema, maxItems int) *Schema {
	return &Schema{Type: "array", Description: desc, Items: item, MaxItems: intPtr(maxItems)}
}

// JSONSchema renders this schema in the shape a provider expects on the wire.
//
// The `required` key is always an array, never null. A tool with no required
// fields used to emit `"required": null`, which is not valid JSON Schema: the
// Claude API tolerated it, and DeepSeek — correctly — rejects the whole request
// with "null is not of type array". One malformed tool takes down every turn, so
// the wire shape is built here rather than at each call site.
func (s *Schema) JSONSchema() map[string]any {
	if s == nil {
		return map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}}
	}
	props := map[string]any{}
	for name, p := range s.Properties {
		props[name] = p.asMap()
	}
	required := s.Required
	if required == nil {
		required = []string{}
	}
	out := map[string]any{"type": "object", "properties": props, "required": required}
	if s.AdditionalProperties != nil {
		out["additionalProperties"] = *s.AdditionalProperties
	}
	return out
}

func (s *Schema) asMap() map[string]any {
	b, err := json.Marshal(s)
	if err != nil {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return map[string]any{}
	}
	return m
}

// ValidationError names the field and says what would fix it. Tool errors are
// read by the model, so "expected one of a, b, c" recovers on the next turn
// while "invalid input" does not.
type ValidationError struct {
	Path    string
	Code    string
	Message string
}

func (e ValidationError) Error() string {
	return fmt.Sprintf("%s at %s: %s", e.Code, e.Path, e.Message)
}

type ValidationErrors []ValidationError

func (v ValidationErrors) Error() string {
	parts := make([]string, len(v))
	for i, e := range v {
		parts[i] = e.Error()
	}
	return "ARGUMENT_INVALID: " + strings.Join(parts, "; ")
}

// Validate checks raw JSON against the schema and returns every problem at
// once, not just the first, so one round trip fixes the whole call.
func Validate(s *Schema, raw json.RawMessage) (map[string]any, error) {
	var v any
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if len(raw) == 0 {
		v = map[string]any{}
	} else if err := dec.Decode(&v); err != nil {
		return nil, ValidationErrors{{Path: "$", Code: "NOT_JSON", Message: err.Error()}}
	}
	var errs ValidationErrors
	validate(s, v, "$", &errs)
	if len(errs) > 0 {
		return nil, errs
	}
	obj, _ := v.(map[string]any)
	if obj == nil {
		obj = map[string]any{}
	}
	applyDefaults(s, obj)
	return obj, nil
}

func applyDefaults(s *Schema, obj map[string]any) {
	for name, ps := range s.Properties {
		if _, present := obj[name]; !present && ps.Default != nil {
			obj[name] = ps.Default
		}
	}
}

func validate(s *Schema, v any, path string, errs *ValidationErrors) {
	if s == nil {
		return
	}
	switch s.Type {
	case "object":
		obj, ok := v.(map[string]any)
		if !ok {
			*errs = append(*errs, ValidationError{path, "TYPE_MISMATCH", "expected an object"})
			return
		}
		for _, req := range s.Required {
			if _, present := obj[req]; !present {
				*errs = append(*errs, ValidationError{path + "." + req, "REQUIRED_MISSING",
					fmt.Sprintf("this field is required; %s", describe(s.Properties[req]))})
			}
		}
		if s.AdditionalProperties != nil && !*s.AdditionalProperties {
			known := make([]string, 0, len(s.Properties))
			for k := range s.Properties {
				known = append(known, k)
			}
			sort.Strings(known)
			for k := range obj {
				if _, ok := s.Properties[k]; !ok {
					*errs = append(*errs, ValidationError{path + "." + k, "UNKNOWN_FIELD",
						fmt.Sprintf("not an accepted field; accepted fields are: %s", strings.Join(known, ", "))})
				}
			}
		}
		for k, val := range obj {
			if ps, ok := s.Properties[k]; ok {
				validate(ps, val, path+"."+k, errs)
			}
		}
	case "string":
		str, ok := v.(string)
		if !ok {
			*errs = append(*errs, ValidationError{path, "TYPE_MISMATCH", "expected a string"})
			return
		}
		if len(s.Enum) > 0 {
			found := false
			for _, e := range s.Enum {
				if e == str {
					found = true
					break
				}
			}
			if !found {
				*errs = append(*errs, ValidationError{path, "ENUM_MISMATCH",
					fmt.Sprintf("got %q; expected one of: %s", str, strings.Join(s.Enum, ", "))})
			}
		}
		if s.MinLength != nil && len([]rune(str)) < *s.MinLength {
			*errs = append(*errs, ValidationError{path, "TOO_SHORT",
				fmt.Sprintf("needs at least %d characters", *s.MinLength)})
		}
		if s.MaxLength != nil && len([]rune(str)) > *s.MaxLength {
			*errs = append(*errs, ValidationError{path, "TOO_LONG",
				fmt.Sprintf("at most %d characters", *s.MaxLength)})
		}
	case "integer", "number":
		n, ok := v.(json.Number)
		if !ok {
			*errs = append(*errs, ValidationError{path, "TYPE_MISMATCH", "expected a number"})
			return
		}
		if s.Type == "integer" {
			if _, err := n.Int64(); err != nil {
				*errs = append(*errs, ValidationError{path, "NOT_INTEGER", "expected a whole number"})
				return
			}
		}
		f, err := n.Float64()
		if err != nil {
			*errs = append(*errs, ValidationError{path, "NOT_NUMBER", err.Error()})
			return
		}
		if s.Minimum != nil && f < *s.Minimum {
			*errs = append(*errs, ValidationError{path, "BELOW_MINIMUM", fmt.Sprintf("must be at least %v", *s.Minimum)})
		}
		if s.Maximum != nil && f > *s.Maximum {
			*errs = append(*errs, ValidationError{path, "ABOVE_MAXIMUM", fmt.Sprintf("must be at most %v", *s.Maximum)})
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			*errs = append(*errs, ValidationError{path, "TYPE_MISMATCH", "expected true or false"})
		}
	case "array":
		arr, ok := v.([]any)
		if !ok {
			*errs = append(*errs, ValidationError{path, "TYPE_MISMATCH", "expected an array"})
			return
		}
		if s.MinItems != nil && len(arr) < *s.MinItems {
			*errs = append(*errs, ValidationError{path, "TOO_FEW_ITEMS", fmt.Sprintf("needs at least %d items", *s.MinItems)})
		}
		if s.MaxItems != nil && len(arr) > *s.MaxItems {
			*errs = append(*errs, ValidationError{path, "TOO_MANY_ITEMS", fmt.Sprintf("at most %d items", *s.MaxItems)})
		}
		for i, item := range arr {
			validate(s.Items, item, fmt.Sprintf("%s[%d]", path, i), errs)
		}
	case "":
		// No declared type means anything goes; used nowhere today, kept so an
		// undeclared type is not silently treated as an object.
	default:
		*errs = append(*errs, ValidationError{path, "SCHEMA_UNSUPPORTED",
			fmt.Sprintf("this build does not validate schema type %q", s.Type)})
	}
}

func describe(s *Schema) string {
	if s == nil {
		return "see the tool description"
	}
	if len(s.Enum) > 0 {
		return "one of: " + strings.Join(s.Enum, ", ")
	}
	if s.Description != "" {
		return s.Description
	}
	return "type " + s.Type
}

// ---- small typed accessors used by the tool implementations ----

func argStr(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return strings.TrimSpace(s)
}

func argBool(m map[string]any, k string) bool {
	b, _ := m[k].(bool)
	return b
}

func argInt(m map[string]any, k string, def int) int {
	n, ok := m[k].(json.Number)
	if !ok {
		return def
	}
	i, err := n.Int64()
	if err != nil {
		return def
	}
	return int(i)
}

func argStrs(m map[string]any, k string) []string {
	arr, _ := m[k].([]any)
	out := make([]string, 0, len(arr))
	for _, v := range arr {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, strings.TrimSpace(s))
		}
	}
	return out
}
