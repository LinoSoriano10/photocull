package webui

import (
	"bytes"
	"encoding/json"
	"image"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"photocull/internal/dedupe"
	"photocull/internal/report"
	"photocull/internal/scanner"
)

// comparePair builds the query for comparing two files under testdata/similar.
func comparePair(endpoint, a, b string) string {
	absA, _ := filepath.Abs(filepath.Join("../../testdata/similar", a))
	absB, _ := filepath.Abs(filepath.Join("../../testdata/similar", b))
	return endpoint + "?a=" + url.QueryEscape(absA) + "&b=" + url.QueryEscape(absB)
}

func getComparison(t *testing.T, s *Server, query string) comparison {
	t.Helper()
	rec := httptest.NewRecorder()
	bound(t, s).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, query, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var out comparison
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func TestCompareReportsHowTwoScannedPhotosDiffer(t *testing.T) {
	s, _ := fixtureServer(t)
	got := getComparison(t, s, comparePair("/api/compare", "source.jpg", "source_resized.jpg"))

	if got.Kind != "image" {
		t.Errorf("kind = %q, want image", got.Kind)
	}
	if got.Note != "" {
		t.Errorf("note = %q, want none: these two photos are comparable", got.Note)
	}
	if got.Image == nil {
		t.Fatal("no image diff returned, so the page has nothing to show above the comparison")
	}
	if got.Image.Ratio > 0.10 {
		t.Errorf("ratio = %.4f: a photo and its half-size copy are being reported as substantially different", got.Image.Ratio)
	}
	if got.Image.Width == 0 || got.Image.Height == 0 {
		t.Errorf("grid = %dx%d, want non-zero: the page cannot place the highlight box without it", got.Image.Width, got.Image.Height)
	}
}

// TestCompareRejectsUnscannedPath is the security test for the pair endpoints.
// Both halves must be checked: validating only the first would leave the second
// as an open door to any file on the disk, and that is the easy mistake here.
func TestCompareRejectsUnscannedPath(t *testing.T) {
	s, _ := fixtureServer(t)
	scanned, _ := filepath.Abs("../../testdata/similar/source.jpg")
	outside := `C:\Windows\win.ini`

	attempts := []struct {
		name  string
		query string
	}{
		{"second path unscanned", "?a=" + url.QueryEscape(scanned) + "&b=" + url.QueryEscape(outside)},
		{"first path unscanned", "?a=" + url.QueryEscape(outside) + "&b=" + url.QueryEscape(scanned)},
		{"both paths unscanned", "?a=" + url.QueryEscape(outside) + "&b=" + url.QueryEscape(outside)},
		{"traversal in the second path", "?a=" + url.QueryEscape(scanned) + "&b=" + url.QueryEscape("../../../../Windows/win.ini")},
		{"no paths at all", ""},
		{"only one path given", "?a=" + url.QueryEscape(scanned)},
	}

	for _, endpoint := range []string{"/api/compare", "/api/imagediff"} {
		for _, tt := range attempts {
			t.Run(endpoint+" "+tt.name, func(t *testing.T) {
				rec := httptest.NewRecorder()
				bound(t, s).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, endpoint+tt.query, nil))
				if rec.Code == http.StatusOK {
					t.Errorf("status = 200; %s must refuse a path the scan never touched", endpoint)
				}
			})
		}
	}
}

func TestImageDiffServesAPNGMatchingTheComparisonGrid(t *testing.T) {
	s, _ := fixtureServer(t)

	rec := httptest.NewRecorder()
	bound(t, s).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, comparePair("/api/imagediff", "source.jpg", "source_recompressed.jpg"), nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("content-type = %q, want image/png", ct)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("the response is not a decodable image: %v", err)
	}
	if format != "png" {
		t.Errorf("format = %q, want png", format)
	}

	// The map must line up with the grid /api/compare reported, or the page
	// would overlay it on the photo at the wrong scale.
	stats := getComparison(t, s, comparePair("/api/compare", "source.jpg", "source_recompressed.jpg"))
	if stats.Image == nil {
		t.Fatal("no image diff to compare the map against")
	}
	if cfg.Width != stats.Image.Width || cfg.Height != stats.Image.Height {
		t.Errorf("map is %dx%d but /api/compare reported %dx%d", cfg.Width, cfg.Height, stats.Image.Width, stats.Image.Height)
	}
}

// TestCompareExplainsAnUndecodablePhoto checks the honest fallback. A file that
// could not be decoded was still grouped by content hash, so it is a real
// duplicate candidate; the view has to say why it cannot show a comparison
// rather than failing and leaving the user with a blank panel.
func TestCompareExplainsAnUndecodablePhoto(t *testing.T) {
	root, err := filepath.Abs("../../testdata")
	if err != nil {
		t.Fatal(err)
	}
	mk := func(rel string, decoded bool) scanner.FileMeta {
		return scanner.FileMeta{
			Path:    filepath.Join(root, rel),
			Size:    1000,
			Decoded: decoded,
			ModTime: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
			SHA256:  rel,
		}
	}
	group := dedupe.Group{
		ID:        "test",
		Type:      dedupe.Exact,
		KeepIndex: 0,
		Files:     []scanner.FileMeta{mk("similar/source.jpg", true), mk("corrupt/truncated.jpg", false)},
	}
	s := New(root, []dedupe.Group{group}, report.Stats{}, &fakeMover{})

	absA := filepath.Join(root, "similar", "source.jpg")
	absB := filepath.Join(root, "corrupt", "truncated.jpg")
	got := getComparison(t, s, "/api/compare?a="+url.QueryEscape(absA)+"&b="+url.QueryEscape(absB))

	if got.Note == "" {
		t.Error("no note explaining why there is no comparison; the page would show an empty panel with no reason")
	}
	if got.Image != nil {
		t.Error("an image diff was returned for a file that could not be decoded")
	}
}
