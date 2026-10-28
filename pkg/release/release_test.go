package release

import (
	"reflect"
	"testing"
)

func TestMergeLabels(t *testing.T) {
	base := map[string]string{"a": "1", "b": "2"}
	got := MergeLabels(base, map[string]string{"b": "", "c": "3"})
	if want := map[string]string{"a": "1", "c": "3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if base["b"] != "2" {
		t.Error("MergeLabels modified its input")
	}
	if got := MergeLabels(map[string]string{"a": "1"}, map[string]string{"a": ""}); got != nil {
		t.Errorf("all removed: %v", got)
	}
}

func TestValidateLabels(t *testing.T) {
	for k, ok := range map[string]bool{
		"managed-by": true, "composecd/app": true, "a.b_c": true, "x": true,
		"": false, "-a": false, "a b": false, "a=b": false, "a,": false,
	} {
		if err := ValidateLabels(map[string]string{k: "v"}); (err == nil) != ok {
			t.Errorf("%q: %v", k, err)
		}
	}
}

func TestParseSelector(t *testing.T) {
	labels := map[string]string{"managed-by": "composecd", "composecd/app": "media"}
	for sel, want := range map[string]bool{
		"":                             true,
		"managed-by=composecd":         true,
		"managed-by==composecd":        true,
		"managed-by=composetpl":        false,
		"managed-by!=composetpl":       true,
		"composecd/app":                true,
		"!composecd/app":               false,
		"!other, managed-by=composecd": true,
		"managed-by=composecd,other=":  false,
		"managed-by=composecd,other!=": true,
	} {
		s, err := ParseSelector(sel)
		if err != nil {
			t.Fatalf("%q: %v", sel, err)
		}
		if got := s(labels); got != want {
			t.Errorf("%q = %v, want %v", sel, got, want)
		}
	}
	for _, bad := range []string{"=x", "a b=c", "!"} {
		if _, err := ParseSelector(bad); err == nil {
			t.Errorf("%q should be invalid", bad)
		}
	}
}
