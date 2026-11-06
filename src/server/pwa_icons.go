package server

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"net/http"
)

// pwaIconAccent is the brand accent colour used as the icon background,
// matching the manifest's theme_color (see handleManifest).
var pwaIconAccent = color.RGBA{R: 0x63, G: 0x66, B: 0xf1, A: 0xff}

// pwaIconMark is the colour of the glyph drawn on top of the accent
// background (a stylised paste/clipboard card).
var pwaIconMark = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}

// generatePWAIconPNG renders a single-colour rounded-square (or, for
// maskable icons, full-bleed square) background with a simple geometric
// "paste card" glyph, and returns the encoded PNG bytes. This avoids any
// CGO-based rasterizer or font/emoji rendering dependency (binary-rules.md
// forbids CGO) by drawing everything with basic geometry on the stdlib
// image/draw canvas.
//
// maskable icons follow the PWA "safe zone" convention: the background
// fills the entire canvas edge-to-edge (no transparency, no rounded
// corners) and the glyph is confined to the inner ~80% so Android adaptive
// icon masks never clip it.
func generatePWAIconPNG(size int, maskable bool) []byte {
	img := image.NewRGBA(image.Rect(0, 0, size, size))

	if maskable {
		draw.Draw(img, img.Bounds(), &image.Uniform{C: pwaIconAccent}, image.Point{}, draw.Src)
	} else {
		radius := float64(size) / 6
		drawRoundedSquare(img, 0, 0, size, size, radius, pwaIconAccent)
	}

	// Safe-zone content box: centered, sized relative to canvas. Maskable
	// icons use the standard 80% safe zone; regular icons use a slightly
	// larger box since there's no adaptive-mask clipping risk.
	safeFrac := 0.62
	if maskable {
		safeFrac = 0.50
	}
	boxSize := float64(size) * safeFrac
	boxX := (float64(size) - boxSize) / 2
	boxY := (float64(size) - boxSize*1.15) / 2
	cardW := boxSize
	cardH := boxSize * 1.15
	cardRadius := cardW / 8

	drawRoundedSquareF(img, boxX, boxY, boxX+cardW, boxY+cardH, cardRadius, pwaIconMark)

	// Small clip tab at the top-center of the card, echoing a clipboard clip.
	tabW := cardW * 0.34
	tabH := cardH * 0.12
	tabX := boxX + (cardW-tabW)/2
	tabY := boxY - tabH*0.35
	drawRoundedSquareF(img, tabX, tabY, tabX+tabW, tabY+tabH, tabH/2, pwaIconAccent)

	// Three accent "text lines" inside the card, representing pasted text.
	lineH := cardH * 0.07
	lineGap := cardH * 0.16
	lineInsetX := cardW * 0.16
	for i, widthFrac := range []float64{0.68, 0.68, 0.42} {
		ly := boxY + cardH*0.3 + float64(i)*lineGap
		lx0 := boxX + lineInsetX
		lx1 := lx0 + (cardW-2*lineInsetX)*widthFrac
		fillRectF(img, lx0, ly, lx1, ly+lineH, pwaIconAccent)
	}

	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// drawRoundedSquare draws an opaque rounded-rect filled with col into img,
// leaving fully transparent pixels outside the rounded corners.
func drawRoundedSquare(img *image.RGBA, x0, y0, x1, y1 int, radius float64, col color.RGBA) {
	drawRoundedSquareF(img, float64(x0), float64(y0), float64(x1), float64(y1), radius, col)
}

// drawRoundedSquareF is the float-coordinate variant, used both for the
// icon background and for the interior "card" glyph.
func drawRoundedSquareF(img *image.RGBA, x0, y0, x1, y1, radius float64, col color.RGBA) {
	bounds := img.Bounds()
	minX := int(math.Floor(x0))
	minY := int(math.Floor(y0))
	maxX := int(math.Ceil(x1))
	maxY := int(math.Ceil(y1))
	if minX < bounds.Min.X {
		minX = bounds.Min.X
	}
	if minY < bounds.Min.Y {
		minY = bounds.Min.Y
	}
	if maxX > bounds.Max.X {
		maxX = bounds.Max.X
	}
	if maxY > bounds.Max.Y {
		maxY = bounds.Max.Y
	}
	if radius > (x1-x0)/2 {
		radius = (x1 - x0) / 2
	}
	if radius > (y1-y0)/2 {
		radius = (y1 - y0) / 2
	}
	for py := minY; py < maxY; py++ {
		for px := minX; px < maxX; px++ {
			// Sample the pixel center.
			cx := float64(px) + 0.5
			cy := float64(py) + 0.5
			if insideRoundedRect(cx, cy, x0, y0, x1, y1, radius) {
				img.SetRGBA(px, py, col)
			}
		}
	}
}

// fillRectF fills a plain (non-rounded) rectangle, used for the accent
// "text line" strokes inside the glyph card.
func fillRectF(img *image.RGBA, x0, y0, x1, y1 float64, col color.RGBA) {
	drawRoundedSquareF(img, x0, y0, x1, y1, 0, col)
}

// insideRoundedRect reports whether point (px,py) falls within the rounded
// rectangle [x0,y0]-[x1,y1] with corner radius r.
func insideRoundedRect(px, py, x0, y0, x1, y1, r float64) bool {
	if px < x0 || px > x1 || py < y0 || py > y1 {
		return false
	}
	if r <= 0 {
		return true
	}
	// Determine which corner region (if any) the point falls into and
	// test against that corner's circle; everywhere else in the rect is
	// automatically inside since it's between the corner insets.
	switch {
	case px < x0+r && py < y0+r:
		return withinCircle(px, py, x0+r, y0+r, r)
	case px > x1-r && py < y0+r:
		return withinCircle(px, py, x1-r, y0+r, r)
	case px < x0+r && py > y1-r:
		return withinCircle(px, py, x0+r, y1-r, r)
	case px > x1-r && py > y1-r:
		return withinCircle(px, py, x1-r, y1-r, r)
	default:
		return true
	}
}

func withinCircle(px, py, cx, cy, r float64) bool {
	dx := px - cx
	dy := py - cy
	return dx*dx+dy*dy <= r*r
}

// servePWAIconPNG writes a generated PWA icon PNG with a long-lived cache
// header (icons are static per build/version).
func servePWAIconPNG(w http.ResponseWriter, size int, maskable bool) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(generatePWAIconPNG(size, maskable))
}
