// Package server: branding image sources, scaling, caching, and remote
// fetching (AI.md PART 16 "Image Sources" / "Image Scaling" / "Remote URL
// Fetching", lines 25848-26035). Resolves server.branding.favicon,
// server.branding.logo, and server.seo.og_image — each may be an empty
// string (embedded default), a local absolute file path, or a remote
// https:// URL — into a set of cached, pre-scaled PNG files on disk so HTTP
// handlers can serve them without re-decoding on every request.
package server

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/apimgr/cairnbin/src/common/urlutil"
)

// brandingSourceKind identifies where a configured branding image value
// resolves from, per the PART 16 Image Sources table.
type brandingSourceKind int

const (
	brandingSourceEmbedded brandingSourceKind = iota
	brandingSourceLocal
	brandingSourceRemote
)

// classifyBrandingSource resolves a raw server.branding.*/server.seo.og_image
// config value into its source kind: empty -> embedded default, an
// http(s):// URL -> remote fetch, anything else -> local absolute file path.
func classifyBrandingSource(configuredValue string) (brandingSourceKind, string) {
	v := strings.TrimSpace(configuredValue)
	if v == "" {
		return brandingSourceEmbedded, ""
	}
	if strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://") {
		return brandingSourceRemote, v
	}
	return brandingSourceLocal, v
}

// brandingImageCacheDir returns the on-disk directory where scaled branding
// PNGs are cached, creating it (0700, service-account-private data) if
// missing.
func brandingImageCacheDir(s *Server) (string, error) {
	dir := filepath.Join(s.dataDir, "branding")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating branding cache dir: %w", err)
	}
	return dir, nil
}

// loadBrandingImageBytes reads the source image bytes for a local file or
// fetches them from a remote URL under SSRF-safe constraints. Callers treat
// any error as "source unavailable" and fall back to the embedded default
// per PART 16's "fallback to embedded default if remote URL fails" rule.
func loadBrandingImageBytes(ctx context.Context, kind brandingSourceKind, value string, fetchCfg urlutil.FetchRemoteImageConfig) ([]byte, error) {
	switch kind {
	case brandingSourceLocal:
		data, err := os.ReadFile(value)
		if err != nil {
			return nil, fmt.Errorf("reading local branding image %q: %w", value, err)
		}
		return data, nil
	case brandingSourceRemote:
		data, _, err := urlutil.FetchRemoteImage(ctx, value, fetchCfg)
		if err != nil {
			return nil, fmt.Errorf("fetching remote branding image %q: %w", value, err)
		}
		return data, nil
	default:
		return nil, fmt.Errorf("embedded source has no bytes to load")
	}
}

// decodeImage decodes PNG/JPEG/GIF source bytes using only the Go stdlib
// (no golang.org/x/image, no CGO). Other formats accepted by the SSRF fetch
// allowlist (e.g. image/webp, image/x-icon) are not stdlib-decodable and are
// treated as a decode failure here, triggering the embedded-default
// fallback in refreshBrandingField.
func decodeImage(data []byte) (image.Image, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decoding image: %w", err)
	}
	return img, nil
}

// resizeNearest scales src to exactly w x h using nearest-neighbor sampling
// (no external dependency required; branding images are small and this runs
// at most once per configured refresh interval, not per-request).
func resizeNearest(src image.Image, w, h int) *image.RGBA {
	if w <= 0 {
		w = 1
	}
	if h <= 0 {
		h = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	if sw <= 0 || sh <= 0 {
		return dst
	}
	for y := 0; y < h; y++ {
		sy := sb.Min.Y + y*sh/h
		for x := 0; x < w; x++ {
			sx := sb.Min.X + x*sw/w
			dst.Set(x, y, src.At(sx, sy))
		}
	}
	return dst
}

// resizeContain scales src to fit entirely within canvasW x canvasH while
// preserving aspect ratio, centering it on a transparent canvas (PART 16
// "preserve aspect ratio" scaling rule) — used for square favicon sizes and
// the 1200x630 OpenGraph image.
func resizeContain(src image.Image, canvasW, canvasH int) *image.RGBA {
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, canvasW, canvasH))
	if sw <= 0 || sh <= 0 || canvasW <= 0 || canvasH <= 0 {
		return dst
	}
	scale := float64(canvasW) / float64(sw)
	if alt := float64(canvasH) / float64(sh); alt < scale {
		scale = alt
	}
	fitW := int(float64(sw) * scale)
	fitH := int(float64(sh) * scale)
	if fitW < 1 {
		fitW = 1
	}
	if fitH < 1 {
		fitH = 1
	}
	scaled := resizeNearest(src, fitW, fitH)
	offX := (canvasW - fitW) / 2
	offY := (canvasH - fitH) / 2
	for y := 0; y < fitH; y++ {
		for x := 0; x < fitW; x++ {
			dst.Set(offX+x, offY+y, scaled.At(x, y))
		}
	}
	return dst
}

// resizeByWidth scales src to the given target width, preserving aspect
// ratio, for the Logo size set (original / 200px header / 50px mobile).
func resizeByWidth(src image.Image, width int) *image.RGBA {
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	if sw <= 0 {
		return resizeNearest(src, width, width)
	}
	height := int(float64(sh) * float64(width) / float64(sw))
	if height < 1 {
		height = 1
	}
	return resizeNearest(src, width, height)
}

// embeddedDefaultBrandingImage renders the built-in generated glyph (reusing
// pwa_icons.go's generatePWAIconPNG) at the requested dimensions, letterboxed
// onto a non-square canvas when needed. This is the PART 16 "Embedded
// default" source and the universal fallback when a local/remote source is
// configured but fails to load or decode.
func embeddedDefaultBrandingImage(w, h int) image.Image {
	base := w
	if h > base {
		base = h
	}
	if base < 16 {
		base = 16
	}
	data := generatePWAIconPNG(base, false)
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return image.NewRGBA(image.Rect(0, 0, w, h))
	}
	if w == h {
		if img.Bounds().Dx() == w && img.Bounds().Dy() == h {
			return img
		}
		return resizeNearest(img, w, h)
	}
	return resizeContain(img, w, h)
}

// faviconSizes are the required favicon raster sizes (PART 16 Image Scaling
// table): 16/32/48 classic favicon, 180 apple-touch-icon, 192/512 PWA.
var faviconSizes = []int{16, 32, 48, 180, 192, 512}

// logoWidths are the required logo widths (PART 16 Image Scaling table):
// original (0 = unscaled), 200px header, 50px mobile.
var logoWidths = []int{0, 200, 50}

const (
	ogImageWidth  = 1200
	ogImageHeight = 630
)

// brandingCacheFileName returns the on-disk cache file name for one rendered
// variant of a branding field.
func brandingCacheFileName(field, variant string) string {
	return fmt.Sprintf("%s-%s.png", field, variant)
}

// encodePNG encodes img as PNG bytes.
func encodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("encoding PNG: %w", err)
	}
	return buf.Bytes(), nil
}

// refreshBrandingField resolves one branding field (favicon/logo/og_image),
// renders every required variant, and writes each as a cached PNG file under
// the branding cache directory. Local-read and remote-fetch/decode failures
// fall back to the embedded default per PART 16 and are logged as a WARN,
// never returned as a hard error — only genuine internal failures (cache
// directory unwritable) propagate.
func (s *Server) refreshBrandingField(ctx context.Context, field, configuredValue string, variants map[string][2]int, fetchCfg urlutil.FetchRemoteImageConfig) error {
	cacheDir, err := brandingImageCacheDir(s)
	if err != nil {
		return err
	}

	kind, value := classifyBrandingSource(configuredValue)
	var src image.Image
	if kind != brandingSourceEmbedded {
		data, loadErr := loadBrandingImageBytes(ctx, kind, value, fetchCfg)
		if loadErr != nil {
			log.Printf("branding: %s source unavailable, using embedded default: %v", field, loadErr)
		} else {
			decoded, decodeErr := decodeImage(data)
			if decodeErr != nil {
				log.Printf("branding: %s source undecodable, using embedded default: %v", field, decodeErr)
			} else {
				src = decoded
			}
		}
	}

	for variant, dims := range variants {
		w, h := dims[0], dims[1]
		var rendered image.Image
		if src != nil {
			if w == 0 {
				// Original-size logo variant: keep source dimensions.
				rendered = src
			} else if h == 0 {
				rendered = resizeByWidth(src, w)
			} else {
				rendered = resizeContain(src, w, h)
			}
		} else {
			outW, outH := w, h
			if outW == 0 {
				outW = 200
			}
			if outH == 0 {
				outH = outW
			}
			rendered = embeddedDefaultBrandingImage(outW, outH)
		}

		data, encErr := encodePNG(rendered)
		if encErr != nil {
			return fmt.Errorf("%s/%s: %w", field, variant, encErr)
		}
		path := filepath.Join(cacheDir, brandingCacheFileName(field, variant))
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return fmt.Errorf("writing %s/%s: %w", field, variant, err)
		}
	}
	return nil
}

// faviconVariants/logoVariants/ogImageVariants map each cache-file variant
// name to its target [width,height] (height 0 for logo means "scale by
// width, preserve aspect ratio"; width 0 for the logo "original" variant
// means "keep source dimensions, or a 200x200 square when using the
// embedded default").
func faviconVariants() map[string][2]int {
	m := make(map[string][2]int, len(faviconSizes))
	for _, size := range faviconSizes {
		m[fmt.Sprintf("%d", size)] = [2]int{size, size}
	}
	return m
}

func logoVariants() map[string][2]int {
	m := make(map[string][2]int, len(logoWidths))
	for _, width := range logoWidths {
		name := "original"
		if width > 0 {
			name = fmt.Sprintf("%d", width)
		}
		m[name] = [2]int{width, 0}
	}
	return m
}

func ogImageVariants() map[string][2]int {
	return map[string][2]int{
		"1200x630": {ogImageWidth, ogImageHeight},
	}
}

// RefreshBrandingImages resolves and caches the favicon, logo, and OG-image
// branding fields (server.branding.favicon/.logo, server.seo.og_image) at
// every required PART 16 size, falling back to the embedded generated
// default on any per-field local-read/remote-fetch/decode failure. Wired
// into the "branding_refresh" scheduler task, mirroring UpdateGeoIP's
// structure.
func (s *Server) RefreshBrandingImages() error {
	cfg := s.liveCfg()
	fetchCfg := urlutil.DefaultFetchRemoteImageConfig()
	ctx, cancel := context.WithTimeout(context.Background(), fetchCfg.Timeout*3)
	defer cancel()

	var errs []string
	if err := s.refreshBrandingField(ctx, "favicon", cfg.Server.Branding.Favicon, faviconVariants(), fetchCfg); err != nil {
		errs = append(errs, "favicon: "+err.Error())
	}
	if err := s.refreshBrandingField(ctx, "logo", cfg.Server.Branding.Logo, logoVariants(), fetchCfg); err != nil {
		errs = append(errs, "logo: "+err.Error())
	}
	if err := s.refreshBrandingField(ctx, "og-image", cfg.Server.SEO.OGImage, ogImageVariants(), fetchCfg); err != nil {
		errs = append(errs, "og_image: "+err.Error())
	}
	if len(errs) > 0 {
		return fmt.Errorf("branding image refresh: %s", strings.Join(errs, "; "))
	}
	return nil
}

// brandingCachedPNG returns the cached PNG bytes for one branding
// field/variant, generating it on demand (synchronous refresh of just that
// field) on a cache miss so first-request latency stays bounded to one
// field's worth of work instead of a full RefreshBrandingImages() pass.
func (s *Server) brandingCachedPNG(field, variant string, variants map[string][2]int, configuredValue string) ([]byte, error) {
	cacheDir, err := brandingImageCacheDir(s)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(cacheDir, brandingCacheFileName(field, variant))
	if data, err := os.ReadFile(path); err == nil {
		return data, nil
	}

	fetchCfg := urlutil.DefaultFetchRemoteImageConfig()
	ctx, cancel := context.WithTimeout(context.Background(), fetchCfg.Timeout)
	defer cancel()
	if err := s.refreshBrandingField(ctx, field, configuredValue, variants, fetchCfg); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}
