package provider

import (
	"reflect"
	"testing"
)

func TestParseDiscoveredModelsAddsCompatibilityModels(t *testing.T) {
	got := parseDiscoveredModels("MODEL\nAuto\nPerformance\n")
	want := []string{"Auto", "Performance", "Aria", "Cantus"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseDiscoveredModels() = %#v, want %#v", got, want)
	}
}

func TestParseDiscoveredModelsDoesNotDuplicateCompatibilityModels(t *testing.T) {
	got := parseDiscoveredModels("Available models:\n- Auto\n- Aria\n- Cantus\n")
	want := []string{"Auto", "Aria", "Cantus"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseDiscoveredModels() = %#v, want %#v", got, want)
	}
}
