package provider

import (
	"reflect"
	"testing"
)

func TestParseDiscoveredModelsAddsAriaCompatibilityModel(t *testing.T) {
	got := parseDiscoveredModels("MODEL\nAuto\nPerformance\n")
	want := []string{"Auto", "Performance", "Aria"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseDiscoveredModels() = %#v, want %#v", got, want)
	}
}

func TestParseDiscoveredModelsDoesNotDuplicateAria(t *testing.T) {
	got := parseDiscoveredModels("Available models:\n- Auto\n- Aria\n")
	want := []string{"Auto", "Aria"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseDiscoveredModels() = %#v, want %#v", got, want)
	}
}
