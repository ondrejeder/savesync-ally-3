package platform

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
)

// IconPNG renders a raster version of IconSVG (blue rounded square with a
// white cloud) for places that cannot use SVG, e.g. Windows notifications.
func IconPNG(size int) []byte {
	const ss = 4 // supersampling for smooth edges
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	blue := color.NRGBA{0x25, 0x63, 0xeb, 0xff}
	scale := 64.0 / float64(size)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var bg, fg int
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					// sample position in the 64x64 SVG coordinate space
					px := (float64(x) + (float64(sx)+0.5)/ss) * scale
					py := (float64(y) + (float64(sy)+0.5)/ss) * scale
					if !inRoundRect(px, py, 64, 14) {
						continue
					}
					bg++
					if inCloud(px, py) {
						fg++
					}
				}
			}
			total := ss * ss
			if bg == 0 {
				continue
			}
			// blend white over blue by cloud coverage, alpha by square coverage
			t := float64(fg) / float64(bg)
			mix := func(c uint8) uint8 { return uint8(math.Round(float64(c)*(1-t) + 255*t)) }
			img.SetNRGBA(x, y, color.NRGBA{mix(blue.R), mix(blue.G), mix(blue.B), uint8(255 * bg / total)})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func inRoundRect(x, y, size, r float64) bool {
	cx := math.Max(r, math.Min(size-r, x))
	cy := math.Max(r, math.Min(size-r, y))
	return (x-cx)*(x-cx)+(y-cy)*(y-cy) <= r*r
}

// inCloud approximates the cloud path of IconSVG with circles and a base.
func inCloud(x, y float64) bool {
	in := func(cx, cy, r float64) bool { return (x-cx)*(x-cx)+(y-cy)*(y-cy) <= r*r }
	return in(33, 30, 12) || in(45, 34, 10) || in(21, 35.5, 8.5) ||
		(x >= 21 && x <= 45 && y >= 30 && y <= 44)
}
