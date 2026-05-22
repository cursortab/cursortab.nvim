package assert

import (
	"reflect"
	"strings"
	"testing"
)

// Equal compares two values and reports any differences
func Equal(t *testing.T, expected, actual any, label string) {
	t.Helper()
	if !deepEqual(expected, actual) {
		t.Errorf("Expected %v, got %v for %s", expected, actual, label)
	}
}

// deepEqual compares two values, handling numeric type conversions
func deepEqual(expected, actual any) bool {
	if reflect.DeepEqual(expected, actual) {
		return true
	}
	// Handle numeric type conversions
	ev := reflect.ValueOf(expected)
	av := reflect.ValueOf(actual)
	if isNumeric(ev.Kind()) && isNumeric(av.Kind()) {
		return toFloat64(ev) == toFloat64(av)
	}
	return false
}

func isNumeric(k reflect.Kind) bool {
	switch k {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	}
	return false
}

func toFloat64(v reflect.Value) float64 {
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(v.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return float64(v.Uint())
	case reflect.Float32, reflect.Float64:
		return v.Float()
	}
	return 0
}

// NotNil fails if value is nil or an empty slice/map
func NotNil(t *testing.T, value any, label string) {
	t.Helper()
	if isNilOrEmpty(value) {
		t.Errorf("Expected non-nil value for %s", label)
	}
}

// Nil fails if value is not nil (handles slices/maps correctly)
func Nil(t *testing.T, value any, label string) {
	t.Helper()
	if !isNilOrEmpty(value) {
		t.Errorf("Expected nil for %s, got %v", label, value)
	}
}

// isNilOrEmpty checks if value is nil or an empty slice/map
func isNilOrEmpty(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Slice, reflect.Map, reflect.Chan:
		return v.IsNil() || v.Len() == 0
	case reflect.Pointer, reflect.Interface, reflect.Func:
		return v.IsNil()
	}
	return false
}

// True fails if value is not true
func True(t *testing.T, value bool, label string) {
	t.Helper()
	if !value {
		t.Errorf("Expected true for %s", label)
	}
}

// False fails if value is not false
func False(t *testing.T, value bool, label string) {
	t.Helper()
	if value {
		t.Errorf("Expected false for %s", label)
	}
}

// Len checks that the length equals expected, fails fatally if not (for safe array access)
func Len(t *testing.T, expected int, collection any, label string) {
	t.Helper()
	v := reflect.ValueOf(collection)
	if v.Kind() != reflect.Slice && v.Kind() != reflect.Array && v.Kind() != reflect.Map {
		t.Fatalf("Len requires slice/array/map, got %v for %s", v.Kind(), label)
	}
	if v.Len() != expected {
		t.Fatalf("Expected length %d, got %d for %s", expected, v.Len(), label)
	}
}

// NotEqual checks that two values are not equal
func NotEqual(t *testing.T, unexpected, actual any, label string) {
	t.Helper()
	if deepEqual(unexpected, actual) {
		t.Errorf("Expected value different from %v for %s", unexpected, label)
	}
}

// Contains checks if a string contains a substring
func Contains(t *testing.T, haystack, needle string, label string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Errorf("Expected %q to contain %q for %s", haystack, needle, label)
	}
}

// Error checks that an error is not nil
func Error(t *testing.T, err error, label string) {
	t.Helper()
	if err == nil {
		t.Errorf("Expected error for %s", label)
	}
}

// NoError checks that an error is nil
func NoError(t *testing.T, err error, label string) {
	t.Helper()
	if err != nil {
		t.Errorf("Expected no error for %s, got: %v", label, err)
	}
}

// Greater checks that actual > expected
func Greater(t *testing.T, actual, expected int, label string) {
	t.Helper()
	if actual <= expected {
		t.Errorf("Expected %d > %d for %s", actual, expected, label)
	}
}

// GreaterOrEqual checks that actual >= expected
func GreaterOrEqual(t *testing.T, actual, expected int, label string) {
	t.Helper()
	if actual < expected {
		t.Errorf("Expected %d >= %d for %s", actual, expected, label)
	}
}

// Less checks that actual < expected
func Less(t *testing.T, actual, expected int, label string) {
	t.Helper()
	if actual >= expected {
		t.Errorf("Expected %d < %d for %s", actual, expected, label)
	}
}

// LessOrEqual checks that actual <= expected
func LessOrEqual(t *testing.T, actual, expected int, label string) {
	t.Helper()
	if actual > expected {
		t.Errorf("Expected %d <= %d for %s", actual, expected, label)
	}
}
