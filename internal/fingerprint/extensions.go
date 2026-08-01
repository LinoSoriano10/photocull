package fingerprint

import (
	"path/filepath"
	"strings"
)

// NormaliseExtensions lowercases extensions and makes sure each starts with a
// dot, so "--ext JPG" and "--ext .jpg" mean the same thing.
func NormaliseExtensions(exts []string) map[string]bool {
	set := make(map[string]bool, len(exts))
	for _, ext := range exts {
		ext = strings.ToLower(strings.TrimSpace(ext))
		if ext == "" {
			continue
		}
		if !strings.HasPrefix(ext, ".") {
			ext = "." + ext
		}
		set[ext] = true
	}
	return set
}

// HasExtension reports whether path carries one of the accepted extensions.
func HasExtension(path string, accepted map[string]bool) bool {
	return accepted[strings.ToLower(filepath.Ext(path))]
}
