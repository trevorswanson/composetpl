package values

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCoalesce(t *testing.T) {
	defaults := map[string]interface{}{
		"global": map[string]interface{}{"tz": "UTC", "puid": 1000.0},
		"apps": map[string]interface{}{
			"plex":   map[string]interface{}{"enabled": true, "image": "plex"},
			"sonarr": map[string]interface{}{"enabled": true},
		},
	}
	user := map[string]interface{}{
		"global": map[string]interface{}{"tz": "America/Chicago"},
		"apps": map[string]interface{}{
			"plex":   map[string]interface{}{"enabled": false},
			"sonarr": nil,
			"radarr": map[string]interface{}{"enabled": true, "gone": nil},
		},
	}
	got := Coalesce(user, defaults)
	want := map[string]interface{}{
		"global": map[string]interface{}{"tz": "America/Chicago", "puid": 1000.0},
		"apps": map[string]interface{}{
			"plex":   map[string]interface{}{"enabled": false, "image": "plex"},
			"radarr": map[string]interface{}{"enabled": true},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Coalesce:\n got %#v\nwant %#v", got, want)
	}
	if _, ok := defaults["apps"].(map[string]interface{})["sonarr"]; !ok {
		t.Fatal("Coalesce modified its defaults input")
	}
}

func TestMergeValuesOrder(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.yaml")
	b := filepath.Join(dir, "b.yaml")
	secret := filepath.Join(dir, "secret.txt")
	writeFile(t, a, "x: 1\nnested: {a: 1, b: 1}\n")
	writeFile(t, b, "x: 2\nnested: {b: 2}\n")
	writeFile(t, secret, "hunter2")

	got, err := Options{
		ValueFiles:   []string{a, b},
		Values:       []string{"nested.c=3", "list={p,q}"},
		StringValues: []string{"port=8080"},
		FileValues:   []string{"pw=" + secret},
	}.MergeValues()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]interface{}{
		"x":      2.0,
		"nested": map[string]interface{}{"a": 1.0, "b": 2.0, "c": int64(3)},
		"list":   []interface{}{"p", "q"},
		"port":   "8080",
		"pw":     "hunter2",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MergeValues:\n got %#v\nwant %#v", got, want)
	}
}

func writeFile(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}
