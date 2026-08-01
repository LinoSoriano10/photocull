package fingerprint

import "testing"

func TestNormaliseExtensions(t *testing.T) {
	set := NormaliseExtensions([]string{"JPG", ".Jpeg", "  png ", "", "heic"})

	for _, want := range []string{".jpg", ".jpeg", ".png", ".heic"} {
		if !set[want] {
			t.Errorf("normalised set missing %q; got %v", want, set)
		}
	}
	if set[""] {
		t.Error("empty extension should be dropped")
	}
}

func TestHasExtension(t *testing.T) {
	set := NormaliseExtensions(Default().Extensions())

	cases := map[string]bool{
		"photo.jpg":   true,
		"PHOTO.JPG":   true,
		"clip.HEIC":   true,
		"scan.heif":   true,
		"notes.txt":   false,
		"archive.zip": false,
		"noext":       false,
	}
	for path, want := range cases {
		if got := HasExtension(path, set); got != want {
			t.Errorf("HasExtension(%q) = %v, want %v", path, got, want)
		}
	}
}
