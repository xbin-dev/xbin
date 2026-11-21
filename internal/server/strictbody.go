package server

// strictbody.go — a body a person's agent files for its person's approval
// is read one way only (D252), as an MCP call's always was (mcpjson.go,
// D239). encoding/json matches keys without regard to case and keeps the
// last of two equal keys; a sheet reads a body's keys as they are written.
// So {"message": "Tidy README", "Prompt": "…"} was one brief on the sheet
// and another to the session (v0.3.80–v0.3.84). Every body xbind describes
// to a person — a start, a message, a new tile, a page to read, a change
// set's lines — is read strictly when it is filed: one JSON object, UTF-8,
// every key one the kind declares, in its case, none twice at any depth,
// nothing after it. A refusal says which key, so the agent can file it
// again as it should be.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"
	"unicode/utf8"
)

// StrictDecode decodes raw into v, a pointer to a struct, read as
// strictObject reads a body — one JSON object, UTF-8, no key twice in any
// object at any depth, nothing after it — with every key, wherever v's
// type describes the object (its fields, theirs, a slice's elements), one
// of the JSON names the type declares, exact case. A map's keys are its own
// (checked for repeats only), as is anything in a json.RawMessage or an
// interface value. A value of the wrong type (an object where a string
// goes) is the decoder's error.
func StrictDecode(raw []byte, v any) error {
	if !utf8.Valid(raw) {
		return errors.New("not UTF-8")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	tree, err := strictValue(dec, 0)
	if err != nil {
		return err
	}
	if _, ok := tree.(map[string]any); !ok {
		return errors.New("not one JSON object")
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("something after the JSON object")
	}
	if err := strictShape(tree, reflect.TypeOf(v), ""); err != nil {
		return err
	}
	typed := json.NewDecoder(bytes.NewReader(raw))
	typed.DisallowUnknownFields()
	if err := typed.Decode(v); err != nil {
		var te *json.UnmarshalTypeError
		if errors.As(err, &te) && te.Field != "" {
			return fmt.Errorf("the member %q is %s, not %s", te.Field, article(te.Value), article(jsonKind(te.Type)))
		}
		return err
	}
	return nil
}

var rawMessageType = reflect.TypeOf(json.RawMessage(nil))

// strictShape checks v (a strictValue) against t: each object a struct
// describes has only the struct's JSON names as keys.
func strictShape(v any, t reflect.Type, at string) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if v == nil || t == rawMessageType {
		return nil
	}
	switch t.Kind() {
	case reflect.Struct:
		obj, ok := v.(map[string]any)
		if !ok {
			return nil // not an object: the decoder says what it is instead
		}
		names := jsonFields(t)
		for _, k := range strictKeys(obj) {
			ft, ok := names[k]
			if !ok {
				return fmt.Errorf("an unknown member %q%s (its members: %s — each once, written exactly so)", k, where(at), strings.Join(strictKeys(names), ", "))
			}
			if err := strictShape(obj[k], ft, at+"."+k); err != nil {
				return err
			}
		}
	case reflect.Map:
		obj, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		for _, k := range strictKeys(obj) {
			if err := strictShape(obj[k], t.Elem(), at+"."+k); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		arr, ok := v.([]any)
		if !ok {
			return nil
		}
		for i, e := range arr {
			if err := strictShape(e, t.Elem(), fmt.Sprintf("%s[%d]", at, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

// jsonFields are the JSON names a struct's fields decode from, as
// encoding/json names them (an embedded struct's promoted, an outer field
// winning), each with its field's type.
func jsonFields(t reflect.Type) map[string]reflect.Type {
	out := map[string]reflect.Type{}
	var promoted []map[string]reflect.Type
	for i := range t.NumField() {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if f.Anonymous && name == "" {
			et := f.Type
			for et.Kind() == reflect.Pointer {
				et = et.Elem()
			}
			if et.Kind() == reflect.Struct {
				promoted = append(promoted, jsonFields(et))
				continue
			}
		}
		if !f.IsExported() {
			continue
		}
		out[cmpOr(name, f.Name)] = f.Type
	}
	for _, p := range promoted {
		for k, ft := range p {
			if _, ok := out[k]; !ok {
				out[k] = ft
			}
		}
	}
	return out
}

func strictKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func where(at string) string {
	if at == "" {
		return ""
	}
	return " in " + strings.TrimPrefix(at, ".")
}

// article is a JSON kind with its article: "an object", "a string".
func article(kind string) string {
	if strings.ContainsAny(kind[:min(1, len(kind))], "aeiou") {
		return "an " + kind
	}
	return "a " + kind
}

// jsonKind is a Go type as JSON calls it.
func jsonKind(t reflect.Type) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
		return "number"
	case reflect.Slice, reflect.Array:
		return "list"
	}
	return "object"
}
