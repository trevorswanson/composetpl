package diff

import (
	"strings"
	"testing"
)

const oldManifest = `name: media
services:
  sonarr:
    image: sonarr:1
    ports:
      - "8989:8989"
  plex:
    image: plex:1
networks:
  proxy:
    name: proxy
`

const newManifest = `name: media
services:
  sonarr:
    image: sonarr:2
    ports:
      - "8989:8989"
  radarr:
    image: radarr:1
networks:
  proxy:
    name: proxy
`

func TestManifests(t *testing.T) {
	changes, err := Manifests(oldManifest, newManifest)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range changes {
		got = append(got, c.Resource+" "+string(c.Op))
	}
	want := "services/plex removed,services/radarr added,services/sonarr changed"
	if strings.Join(got, ",") != want {
		t.Errorf("changes = %v, want %s", got, want)
	}

	var b strings.Builder
	if err := Print(&b, changes, PrintOptions{Release: "media", Context: 3}); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, w := range []string{
		"media, services/plex, removed:\n@@ -1 +0,0 @@\n-image: plex:1\n",
		"media, services/radarr, added:\n@@ -0,0 +1 @@\n+image: radarr:1\n",
		"media, services/sonarr, changed:\n@@ -1,3 +1,3 @@\n-image: sonarr:1\n+image: sonarr:2\n ports:\n",
	} {
		if !strings.Contains(out, w) {
			t.Errorf("output missing %q:\n%s", w, out)
		}
	}
}

func TestManifestsEmptyAndIdentical(t *testing.T) {
	if c, err := Manifests(oldManifest, oldManifest); err != nil || len(c) != 0 {
		t.Errorf("identical: %v %v", c, err)
	}
	c, err := Manifests("", oldManifest)
	if err != nil || len(c) != 4 || c[0].Op != Added {
		t.Errorf("from empty: %+v %v", c, err)
	}
}

func TestColor(t *testing.T) {
	changes, _ := Manifests(oldManifest, newManifest)
	var b strings.Builder
	_ = Print(&b, changes, PrintOptions{Release: "media", Color: true})
	if !strings.Contains(b.String(), "\x1b[32m+image: radarr:1\x1b[0m") {
		t.Errorf("expected coloured additions:\n%q", b.String())
	}
}
