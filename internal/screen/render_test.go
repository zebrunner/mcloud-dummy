package screen

import (
	"image"
	"image/color"
	"testing"
)

func TestTurnClockwise(t *testing.T) {
	// a 3x2 image with a red top left pixel
	src := image.NewRGBA(image.Rect(0, 0, 3, 2))
	red := color.RGBA{0xff, 0, 0, 0xff}
	src.SetRGBA(0, 0, red)
	for _, tc := range []struct {
		deg, w, h, x, y int
	}{
		{0, 3, 2, 0, 0},
		{90, 2, 3, 1, 0},
		{180, 3, 2, 2, 1},
		{270, 2, 3, 0, 2},
	} {
		dst := turnClockwise(src, tc.deg)
		if dst.Bounds().Dx() != tc.w || dst.Bounds().Dy() != tc.h || dst.RGBAAt(tc.x, tc.y) != red {
			t.Errorf("%d: %v, red at %d,%d: %v", tc.deg, dst.Bounds(), tc.x, tc.y, dst.RGBAAt(tc.x, tc.y))
		}
	}
}
