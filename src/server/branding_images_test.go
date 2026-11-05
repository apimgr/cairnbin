package server

// Tests for branding_images.go (AI.md PART 16 "Image Sources"/"Image
// Scaling"/"Remote URL Fetching") — source classification, pure-stdlib
// resize helpers, embedded-default generation, the refresh/cache pipeline,
// and the HTTP handlers wired into server.go's router.

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/apimgr/cairnbin/src/common/urlutil"
	"github.com/apimgr/cairnbin/src/config"
)

func newBrandingTestServer(t *testing.T) *Server {
	t.Helper()
	cfg := config.DefaultConfig()
	return &Server{cfg: cfg, dataDir: t.TempDir()}
}

// solidPNG returns w x h opaque PNG bytes of the given color, for use as a
// stand-in "local file" or "decoded remote" source image.
func solidPNG(t *testing.T, w, h int, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encoding test PNG: %v", err)
	}
	return buf.Bytes()
}

func TestClassifyBrandingSource(t *testing.T) {
	cases := []struct {
		name  string
		value string
		kind  brandingSourceKind
	}{
		{"empty is embedded", "", brandingSourceEmbedded},
		{"whitespace-only is embedded", "   ", brandingSourceEmbedded},
		{"https URL is remote", "https://example.com/logo.png", brandingSourceRemote},
		{"http URL is remote", "http://example.com/logo.png", brandingSourceRemote},
		{"absolute path is local", "/etc/cairnbin/logo.png", brandingSourceLocal},
		{"relative path is local", "logo.png", brandingSourceLocal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, _ := classifyBrandingSource(tc.value)
			if kind != tc.kind {
				t.Errorf("classifyBrandingSource(%q) kind = %v, want %v", tc.value, kind, tc.kind)
			}
		})
	}
}

func TestResizeNearest(t *testing.T) {
	src := solidPNG(t, 10, 10, color.RGBA{255, 0, 0, 255})
	img, err := decodeImage(src)
	if err != nil {
		t.Fatalf("decodeImage: %v", err)
	}
	dst := resizeNearest(img, 20, 5)
	if b := dst.Bounds(); b.Dx() != 20 || b.Dy() != 5 {
		t.Fatalf("resizeNearest size = %dx%d, want 20x5", b.Dx(), b.Dy())
	}
	// Zero/negative dimensions clamp to 1x1 rather than producing an invalid image.
	dst2 := resizeNearest(img, 0, -5)
	if b := dst2.Bounds(); b.Dx() != 1 || b.Dy() != 1 {
		t.Fatalf("resizeNearest(0,-5) size = %dx%d, want 1x1", b.Dx(), b.Dy())
	}
}

func TestResizeContain(t *testing.T) {
	// A wide 100x50 source into a 40x40 square canvas should letterbox,
	// producing a 40x20 scaled image centered vertically.
	src := solidPNG(t, 100, 50, color.RGBA{0, 255, 0, 255})
	img, err := decodeImage(src)
	if err != nil {
		t.Fatalf("decodeImage: %v", err)
	}
	dst := resizeContain(img, 40, 40)
	if b := dst.Bounds(); b.Dx() != 40 || b.Dy() != 40 {
		t.Fatalf("resizeContain canvas size = %dx%d, want 40x40", b.Dx(), b.Dy())
	}
	// Top-left corner should be transparent letterbox padding, not source content.
	if _, _, _, a := dst.At(0, 0).RGBA(); a != 0 {
		t.Errorf("expected transparent letterbox padding at (0,0), got alpha=%d", a)
	}
	// Center should carry the source color.
	cr, cg, cb, _ := dst.At(20, 20).RGBA()
	if cr>>8 != 0 || cg>>8 != 255 || cb>>8 != 0 {
		t.Errorf("expected green at center, got rgb=(%d,%d,%d)", cr>>8, cg>>8, cb>>8)
	}
}

func TestResizeByWidth(t *testing.T) {
	src := solidPNG(t, 200, 100, color.RGBA{0, 0, 255, 255})
	img, err := decodeImage(src)
	if err != nil {
		t.Fatalf("decodeImage: %v", err)
	}
	dst := resizeByWidth(img, 50)
	b := dst.Bounds()
	if b.Dx() != 50 || b.Dy() != 25 {
		t.Fatalf("resizeByWidth(50) size = %dx%d, want 50x25 (aspect preserved)", b.Dx(), b.Dy())
	}
}

func TestEmbeddedDefaultBrandingImage(t *testing.T) {
	img := embeddedDefaultBrandingImage(64, 64)
	if b := img.Bounds(); b.Dx() != 64 || b.Dy() != 64 {
		t.Fatalf("square embedded default size = %dx%d, want 64x64", b.Dx(), b.Dy())
	}
	// Non-square (OG image aspect) letterboxes onto the requested canvas.
	wide := embeddedDefaultBrandingImage(1200, 630)
	if b := wide.Bounds(); b.Dx() != 1200 || b.Dy() != 630 {
		t.Fatalf("wide embedded default size = %dx%d, want 1200x630", b.Dx(), b.Dy())
	}
}

func TestFaviconLogoOGImageVariants(t *testing.T) {
	fv := faviconVariants()
	if len(fv) != len(faviconSizes) {
		t.Fatalf("faviconVariants() has %d entries, want %d", len(fv), len(faviconSizes))
	}
	for _, size := range faviconSizes {
		dims, ok := fv[itoaSEO(size)]
		if !ok {
			t.Fatalf("faviconVariants() missing entry for size %d", size)
		}
		if dims[0] != size || dims[1] != size {
			t.Errorf("faviconVariants()[%d] = %v, want [%d %d]", size, dims, size, size)
		}
	}

	lv := logoVariants()
	if len(lv) != len(logoWidths) {
		t.Fatalf("logoVariants() has %d entries, want %d", len(lv), len(logoWidths))
	}
	if dims, ok := lv["original"]; !ok || dims[0] != 0 {
		t.Errorf(`logoVariants()["original"] = %v, ok=%v, want [0 0]`, dims, ok)
	}
	if dims, ok := lv["200"]; !ok || dims[0] != 200 || dims[1] != 0 {
		t.Errorf(`logoVariants()["200"] = %v, ok=%v, want [200 0]`, dims, ok)
	}
	if dims, ok := lv["50"]; !ok || dims[0] != 50 || dims[1] != 0 {
		t.Errorf(`logoVariants()["50"] = %v, ok=%v, want [50 0]`, dims, ok)
	}

	ov := ogImageVariants()
	if dims, ok := ov["1200x630"]; !ok || dims[0] != ogImageWidth || dims[1] != ogImageHeight {
		t.Errorf(`ogImageVariants()["1200x630"] = %v, ok=%v, want [%d %d]`, dims, ok, ogImageWidth, ogImageHeight)
	}
}

// TestRefreshBrandingField_Embedded verifies the empty-config path renders
// and caches the embedded default for every requested variant.
func TestRefreshBrandingField_Embedded(t *testing.T) {
	s := newBrandingTestServer(t)
	ctx := t.Context()
	fetchCfg := urlutil.DefaultFetchRemoteImageConfig()

	if err := s.refreshBrandingField(ctx, "favicon", "", faviconVariants(), fetchCfg); err != nil {
		t.Fatalf("refreshBrandingField: %v", err)
	}
	cacheDir, err := brandingImageCacheDir(s)
	if err != nil {
		t.Fatalf("brandingImageCacheDir: %v", err)
	}
	for _, size := range faviconSizes {
		path := filepath.Join(cacheDir, brandingCacheFileName("favicon", itoaSEO(size)))
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("expected cached favicon-%d.png: %v", size, err)
		}
		img, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("decoding cached favicon-%d.png: %v", size, err)
		}
		if b := img.Bounds(); b.Dx() != size || b.Dy() != size {
			t.Errorf("favicon-%d.png decoded size = %dx%d, want %dx%d", size, b.Dx(), b.Dy(), size, size)
		}
	}
}

// TestRefreshBrandingField_LocalFile verifies a configured local file path is
// read, decoded, and scaled into every requested logo variant.
func TestRefreshBrandingField_LocalFile(t *testing.T) {
	s := newBrandingTestServer(t)
	srcDir := t.TempDir()
	srcPath := filepath.Join(srcDir, "logo.png")
	if err := os.WriteFile(srcPath, solidPNG(t, 400, 100, color.RGBA{255, 255, 0, 255}), 0o600); err != nil {
		t.Fatalf("writing source logo: %v", err)
	}

	ctx := t.Context()
	fetchCfg := urlutil.DefaultFetchRemoteImageConfig()
	if err := s.refreshBrandingField(ctx, "logo", srcPath, logoVariants(), fetchCfg); err != nil {
		t.Fatalf("refreshBrandingField: %v", err)
	}
	cacheDir, err := brandingImageCacheDir(s)
	if err != nil {
		t.Fatalf("brandingImageCacheDir: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(cacheDir, brandingCacheFileName("logo", "original")))
	if err != nil {
		t.Fatalf("reading cached logo-original.png: %v", err)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decoding cached logo-original.png: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 400 || b.Dy() != 100 {
		t.Errorf("logo-original.png decoded size = %dx%d, want 400x100 (source dimensions kept)", b.Dx(), b.Dy())
	}

	data200, err := os.ReadFile(filepath.Join(cacheDir, brandingCacheFileName("logo", "200")))
	if err != nil {
		t.Fatalf("reading cached logo-200.png: %v", err)
	}
	img200, _, err := image.Decode(bytes.NewReader(data200))
	if err != nil {
		t.Fatalf("decoding cached logo-200.png: %v", err)
	}
	if b := img200.Bounds(); b.Dx() != 200 || b.Dy() != 50 {
		t.Errorf("logo-200.png decoded size = %dx%d, want 200x50 (aspect preserved)", b.Dx(), b.Dy())
	}
}

// TestRefreshBrandingField_LocalFileMissing verifies an unreadable local path
// falls back to the embedded default instead of returning an error (PART 16
// "Fallback to embedded default if remote URL fails" applies equally to a
// missing/unreadable local file).
func TestRefreshBrandingField_LocalFileMissing(t *testing.T) {
	s := newBrandingTestServer(t)
	ctx := t.Context()
	fetchCfg := urlutil.DefaultFetchRemoteImageConfig()

	if err := s.refreshBrandingField(ctx, "favicon", "/nonexistent/does-not-exist.png", faviconVariants(), fetchCfg); err != nil {
		t.Fatalf("refreshBrandingField should fall back to embedded default, got error: %v", err)
	}
	cacheDir, err := brandingImageCacheDir(s)
	if err != nil {
		t.Fatalf("brandingImageCacheDir: %v", err)
	}
	if _, err := os.ReadFile(filepath.Join(cacheDir, brandingCacheFileName("favicon", "32"))); err != nil {
		t.Fatalf("expected embedded-default fallback to still be cached: %v", err)
	}
}

// TestRefreshBrandingImages_AllEmbedded verifies the full three-field
// (favicon/logo/og-image) refresh succeeds against DefaultConfig() (no
// branding fields configured, so every field uses the embedded default).
func TestRefreshBrandingImages_AllEmbedded(t *testing.T) {
	s := newBrandingTestServer(t)
	if err := s.RefreshBrandingImages(); err != nil {
		t.Fatalf("RefreshBrandingImages: %v", err)
	}
	cacheDir, err := brandingImageCacheDir(s)
	if err != nil {
		t.Fatalf("brandingImageCacheDir: %v", err)
	}
	for _, name := range []string{
		brandingCacheFileName("favicon", "32"),
		brandingCacheFileName("logo", "original"),
		brandingCacheFileName("og-image", "1200x630"),
	} {
		if _, err := os.ReadFile(filepath.Join(cacheDir, name)); err != nil {
			t.Errorf("expected cached %s: %v", name, err)
		}
	}
}

// TestBrandingCachedPNG_GeneratesOnMiss verifies a cache miss synchronously
// renders just the requested field/variant rather than requiring a prior
// RefreshBrandingImages() call.
func TestBrandingCachedPNG_GeneratesOnMiss(t *testing.T) {
	s := newBrandingTestServer(t)
	data, err := s.brandingCachedPNG("favicon", "16", faviconVariants(), "")
	if err != nil {
		t.Fatalf("brandingCachedPNG: %v", err)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decoding on-demand favicon-16: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 16 || b.Dy() != 16 {
		t.Errorf("on-demand favicon-16 decoded size = %dx%d, want 16x16", b.Dx(), b.Dy())
	}

	// Second call should hit the now-populated cache file rather than
	// re-rendering (behavior is observationally identical either way, but
	// exercise the cache-hit path for coverage).
	data2, err := s.brandingCachedPNG("favicon", "16", faviconVariants(), "")
	if err != nil {
		t.Fatalf("brandingCachedPNG (cache hit): %v", err)
	}
	if !bytes.Equal(data, data2) {
		t.Errorf("expected identical bytes from cache hit")
	}
}

// TestHandleFavicon_ServesPNG covers the /favicon.ico handler end to end.
func TestHandleFavicon_ServesPNG(t *testing.T) {
	s := newBrandingTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/favicon.ico", nil)
	rec := httptest.NewRecorder()
	s.handleFavicon(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}
	if _, _, err := image.Decode(bytes.NewReader(rec.Body.Bytes())); err != nil {
		t.Errorf("response body is not a decodable image: %v", err)
	}
}

// TestHandleBrandingFavicon_AllSizes covers every registered
// /static/branding/favicon-{size}.png route.
func TestHandleBrandingFavicon_AllSizes(t *testing.T) {
	s := newBrandingTestServer(t)
	for _, size := range faviconSizes {
		name := itoaSEO(size)
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/static/branding/favicon-"+name+".png", nil)
			rec := httptest.NewRecorder()
			s.handleBrandingFavicon(name)(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			img, _, err := image.Decode(bytes.NewReader(rec.Body.Bytes()))
			if err != nil {
				t.Fatalf("decoding response: %v", err)
			}
			if b := img.Bounds(); b.Dx() != size || b.Dy() != size {
				t.Errorf("decoded size = %dx%d, want %dx%d", b.Dx(), b.Dy(), size, size)
			}
		})
	}
}

// TestHandleBrandingLogo_AllVariants covers every registered
// /static/branding/logo-{variant}.png route.
func TestHandleBrandingLogo_AllVariants(t *testing.T) {
	s := newBrandingTestServer(t)
	for _, width := range logoWidths {
		name := "original"
		if width > 0 {
			name = itoaSEO(width)
		}
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/static/branding/logo-"+name+".png", nil)
			rec := httptest.NewRecorder()
			s.handleBrandingLogo(name)(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if _, _, err := image.Decode(bytes.NewReader(rec.Body.Bytes())); err != nil {
				t.Errorf("decoding response: %v", err)
			}
		})
	}
}

// TestHandleBrandingOGImage covers the /static/branding/og-image.png route.
func TestHandleBrandingOGImage(t *testing.T) {
	s := newBrandingTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/static/branding/og-image.png", nil)
	rec := httptest.NewRecorder()
	s.handleBrandingOGImage(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	img, _, err := image.Decode(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if b := img.Bounds(); b.Dx() != ogImageWidth || b.Dy() != ogImageHeight {
		t.Errorf("decoded size = %dx%d, want %dx%d", b.Dx(), b.Dy(), ogImageWidth, ogImageHeight)
	}
}

// TestServeBrandingImage_UnknownVariantFallsBack verifies an unrecognized
// variant name (not present in the variants map, so brandingCachedPNG fails
// to render) still returns a 200 with a usable fallback image rather than a
// 5xx (PART 16 "response is never a 5xx" guarantee documented on
// serveBrandingImage).
func TestServeBrandingImage_UnknownVariantFallsBack(t *testing.T) {
	s := newBrandingTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/static/branding/favicon-999.png", nil)
	rec := httptest.NewRecorder()
	s.serveBrandingImage(rec, req, "favicon", "999", faviconVariants(), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if _, _, err := image.Decode(bytes.NewReader(rec.Body.Bytes())); err != nil {
		t.Errorf("decoding fallback response: %v", err)
	}
}
