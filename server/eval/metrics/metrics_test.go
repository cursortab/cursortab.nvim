package metrics

import (
	"math"
	"testing"
)

func TestChrFIdentical(t *testing.T) {
	s := ChrF("hello world", "hello world")
	if math.Abs(s-100.0) > 0.0001 {
		t.Errorf("identical should be 100, got %v", s)
	}
}

func TestChrFDisjoint(t *testing.T) {
	s := ChrF("abc", "xyz")
	if s >= 10 {
		t.Errorf("disjoint should be near zero, got %v", s)
	}
}

func TestDeltaChrFIgnoresCopyThrough(t *testing.T) {
	before := []string{"a", "b", "c"}
	cand := []string{"a", "B", "c"}
	ref := []string{"a", "B2", "c"}
	d := DeltaChrF(before, cand, ref)
	if d <= 0 || d >= 100 {
		t.Errorf("delta chrF should be partial, got %v", d)
	}
	d2 := DeltaChrF(before, []string{"a", "B2", "c"}, ref)
	if math.Abs(d2-100) > 0.1 {
		t.Errorf("perfect match on delta should be 100, got %v", d2)
	}
}

func TestComputeCombined(t *testing.T) {
	before := []string{"a", "b"}
	cand := []string{"a", "B"}
	ref := []string{"a", "B"}
	s := Compute(before, cand, ref, 150, true)
	if s.DeltaChrF < 99 {
		t.Errorf("perfect delta should be near 100, got %v", s.DeltaChrF)
	}
	if s.Combined < 99 {
		t.Errorf("combined should equal deltaChrF when shown, got %v", s.Combined)
	}
	sHidden := Compute(before, cand, ref, 150, false)
	if sHidden.Combined != 0 {
		t.Errorf("combined should be 0 when not shown, got %v", sHidden.Combined)
	}
}
