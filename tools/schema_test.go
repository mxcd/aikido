package tools

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestObject_Canonical(t *testing.T) {
	raw := Object(map[string]any{
		"x": String("an x value"),
	}, "x")

	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["type"] != "object" {
		t.Errorf("type = %v", got["type"])
	}
	props, _ := got["properties"].(map[string]any)
	x, _ := props["x"].(map[string]any)
	if x["type"] != "string" || x["description"] != "an x value" {
		t.Errorf("x prop = %v", x)
	}
	required, _ := got["required"].([]any)
	if len(required) != 1 || required[0] != "x" {
		t.Errorf("required = %v", required)
	}
}

func TestObject_NoRequired(t *testing.T) {
	raw := Object(map[string]any{"x": Integer("an int")})
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, has := got["required"]; has {
		t.Error("required should be omitted when no required props")
	}
}

func TestObject_NilProps(t *testing.T) {
	raw := Object(nil)
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["type"] != "object" {
		t.Errorf("type = %v", got["type"])
	}
}

func TestSchemaPrimitives(t *testing.T) {
	tests := []struct {
		name string
		got  map[string]any
		want map[string]any
	}{
		{"String", String("d1"), map[string]any{"type": "string", "description": "d1"}},
		{"Integer", Integer("d2"), map[string]any{"type": "integer", "description": "d2"}},
		{"Number", Number("d3"), map[string]any{"type": "number", "description": "d3"}},
		{"Boolean", Boolean("d4"), map[string]any{"type": "boolean", "description": "d4"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !reflect.DeepEqual(tt.got, tt.want) {
				t.Errorf("got %v, want %v", tt.got, tt.want)
			}
		})
	}
}

func TestEnum(t *testing.T) {
	got := Enum("a fruit", "apple", "banana")
	if got["type"] != "string" {
		t.Errorf("type = %v", got["type"])
	}
	values, _ := got["enum"].([]string)
	if len(values) != 2 || values[0] != "apple" || values[1] != "banana" {
		t.Errorf("enum = %v", values)
	}
}

func TestArray(t *testing.T) {
	got := Array(String("element"), "a list")
	if got["type"] != "array" {
		t.Errorf("type = %v", got["type"])
	}
	if got["description"] != "a list" {
		t.Errorf("description = %v", got["description"])
	}
	items, ok := got["items"].(map[string]any)
	if !ok || items["type"] != "string" {
		t.Errorf("items = %v", got["items"])
	}
}
