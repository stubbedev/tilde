package xwayland

import "testing"

func TestMimesAndTargetsMapBothWays(t *testing.T) {
	for _, c := range []struct {
		atom, mime string
	}{
		{"UTF8_STRING", "text/plain;charset=utf-8"},
		{"TEXT", "text/plain"},
		{"image/png", "image/png"},
		{"text/uri-list", "text/uri-list"},
	} {
		if got, ok := mimeOf(c.atom); !ok || got != c.mime {
			t.Errorf("mimeOf(%s) = %q, %v", c.atom, got, ok)
		}
		if got, ok := atomOf(c.mime); !ok || got != c.atom {
			t.Errorf("atomOf(%s) = %q, %v", c.mime, got, ok)
		}
	}
	for _, atom := range []string{"TARGETS", "TIMESTAMP", "MULTIPLE", "STRING"} {
		if mime, ok := mimeOf(atom); ok {
			t.Errorf("X target %s offered to Wayland as %s", atom, mime)
		}
	}
	if name, ok := atomOf("SAVE_TARGETS"); ok {
		t.Errorf("a mime without a slash became the X target %s", name)
	}
}
