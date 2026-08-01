package imageutil

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"os"

	"github.com/nfnt/resize"
)

// Comparison tuning. These are the three numbers that decide whether a
// difference is real, and each one is a judgement about photographs rather than
// about pixels.
const (
	// DiffSize is the longest edge both photos are scaled to before comparing.
	// Comparing at full resolution would be slower for no gain: a difference
	// that survives a human's eye at 900px is the only kind worth flagging, and
	// anything smaller is JPEG noise.
	DiffSize = 900

	// DiffTolerance is how far one colour channel may differ, out of 255,
	// before a pixel counts as changed.
	//
	// It cannot be zero. Two copies of the same photo saved at different JPEG
	// qualities differ slightly in *every* pixel, so a strict comparison would
	// report 100% changed and be useless for exactly the case this view exists
	// to serve. 24 is low enough to catch a watermark or a retouched face and
	// high enough to ignore re-compression.
	DiffTolerance = 24

	// AspectTolerance is how far two aspect ratios may differ and still be
	// treated as the same framing. Beyond it one photo is cropped, the images
	// do not correspond pixel for pixel, and a difference map would be noise
	// dressed up as a finding.
	AspectTolerance = 0.02
)

// Diff is the outcome of comparing two photographs.
type Diff struct {
	// Ratio is the fraction of pixels that differ by more than DiffTolerance.
	Ratio float64

	// Box encloses every differing pixel, in the normalised comparison grid.
	// It is what lets the UI say *where* the change is rather than only how
	// much of it there is.
	Box image.Rectangle

	// Width and Height are the normalised grid both photos were scaled onto.
	Width, Height int

	// AspectMismatch reports that the two photos are framed differently, so no
	// pixel comparison was attempted and Ratio and Box are meaningless.
	AspectMismatch bool

	// a and b are the normalised images, kept so HeatmapPNG does not have to
	// decode and resize all over again.
	a, b *image.RGBA
}

// Compare decodes two photographs, scales them onto a common grid and reports
// how they differ.
//
// Scaling is what makes the comparison possible at all: the photos this view is
// asked about are usually a full-size original and a downscaled copy, which is
// precisely why a content hash could not tell they were related.
func Compare(pathA, pathB string) (*Diff, error) {
	imgA, err := decodeFile(pathA)
	if err != nil {
		return nil, err
	}
	imgB, err := decodeFile(pathB)
	if err != nil {
		return nil, err
	}

	boundsA, boundsB := imgA.Bounds(), imgB.Bounds()
	if boundsA.Dx() == 0 || boundsA.Dy() == 0 || boundsB.Dx() == 0 || boundsB.Dy() == 0 {
		return nil, fmt.Errorf("imageutil: cannot compare an empty image")
	}

	aspectA := float64(boundsA.Dx()) / float64(boundsA.Dy())
	aspectB := float64(boundsB.Dx()) / float64(boundsB.Dy())
	if relativeGap(aspectA, aspectB) > AspectTolerance {
		return &Diff{AspectMismatch: true}, nil
	}

	// Never upscale: comparing invented pixels would manufacture differences
	// that neither photo actually contains.
	edge := min(DiffSize, min(longestEdge(boundsA), longestEdge(boundsB)))
	w, h := fitTo(boundsA, edge)

	normA := toRGBA(resize.Resize(uint(w), uint(h), imgA, resize.Bilinear))
	normB := toRGBA(resize.Resize(uint(w), uint(h), imgB, resize.Bilinear))

	diff := &Diff{Width: w, Height: h, a: normA, b: normB}
	diff.measure()
	return diff, nil
}

// measure walks both normalised images once, counting changed pixels and
// growing the bounding box around them.
func (d *Diff) measure() {
	changed := 0
	box := image.Rectangle{Min: image.Pt(d.Width, d.Height)} // empty, inverted

	for y := 0; y < d.Height; y++ {
		for x := 0; x < d.Width; x++ {
			if channelGap(d.a, d.b, x, y) <= DiffTolerance {
				continue
			}
			changed++
			box.Min.X = min(box.Min.X, x)
			box.Min.Y = min(box.Min.Y, y)
			box.Max.X = max(box.Max.X, x+1)
			box.Max.Y = max(box.Max.Y, y+1)
		}
	}

	if total := d.Width * d.Height; total > 0 {
		d.Ratio = float64(changed) / float64(total)
	}
	if changed > 0 {
		d.Box = box
	}
}

// HeatmapPNG renders the comparison: photo A dimmed to a grey ghost, with the
// pixels that differ lit up in red.
//
// The dimmed original underneath is the point. A bare black-and-red mask tells
// you that something changed somewhere; keeping the photo visible tells you it
// was the face, or the corner where a watermark sits.
func (d *Diff) HeatmapPNG() ([]byte, error) {
	if d.AspectMismatch || d.a == nil || d.b == nil {
		return nil, fmt.Errorf("imageutil: no comparison to render")
	}

	out := image.NewRGBA(image.Rect(0, 0, d.Width, d.Height))
	for y := 0; y < d.Height; y++ {
		for x := 0; x < d.Width; x++ {
			i := d.a.PixOffset(x, y)
			r, g, b := d.a.Pix[i], d.a.Pix[i+1], d.a.Pix[i+2]

			// Rec. 601 luma, then dimmed, so the red has somewhere to stand out.
			grey := uint8((299*int(r) + 587*int(g) + 114*int(b)) / 1000 / 3)

			o := out.PixOffset(x, y)
			out.Pix[o], out.Pix[o+1], out.Pix[o+2], out.Pix[o+3] = grey, grey, grey, 255

			gap := channelGap(d.a, d.b, x, y)
			if gap <= DiffTolerance {
				continue
			}
			// Scale the remaining gap across the full range so a subtle change
			// is still clearly visible rather than a barely-warm pixel.
			heat := min(255, (gap-DiffTolerance)*255/(255-DiffTolerance)+96)
			out.Pix[o] = uint8(heat)
			out.Pix[o+1] = grey / 2
			out.Pix[o+2] = grey / 2
		}
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, out); err != nil {
		return nil, fmt.Errorf("imageutil: encode heatmap: %w", err)
	}
	return buf.Bytes(), nil
}

// channelGap is the largest per-channel difference between the two images at
// one pixel. Taking the maximum rather than the average means a change confined
// to a single channel — a colour cast, a red watermark — is not diluted into
// invisibility by the two channels that did not move.
func channelGap(a, b *image.RGBA, x, y int) int {
	i := a.PixOffset(x, y)
	j := b.PixOffset(x, y)
	gap := 0
	for c := range 3 {
		gap = max(gap, abs(int(a.Pix[i+c])-int(b.Pix[j+c])))
	}
	return gap
}

func decodeFile(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("imageutil: %w", err)
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("imageutil: decode %s: %w", path, err)
	}
	return img, nil
}

// toRGBA gives the comparison a predictable pixel layout, so measure and
// HeatmapPNG can index Pix directly instead of paying for an interface call and
// a 16-bit conversion on every channel of every pixel.
func toRGBA(img image.Image) *image.RGBA {
	if rgba, ok := img.(*image.RGBA); ok {
		return rgba
	}
	b := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(out, out.Bounds(), img, b.Min, draw.Src)
	return out
}

// fitTo returns the size that scales bounds so its longest edge is edge pixels.
func fitTo(bounds image.Rectangle, edge int) (w, h int) {
	if bounds.Dx() >= bounds.Dy() {
		return edge, max(1, bounds.Dy()*edge/bounds.Dx())
	}
	return max(1, bounds.Dx()*edge/bounds.Dy()), edge
}

func longestEdge(b image.Rectangle) int { return max(b.Dx(), b.Dy()) }

// relativeGap compares two ratios proportionally, so the tolerance means the
// same thing for a panorama as for a square.
func relativeGap(a, b float64) float64 {
	if a == 0 || b == 0 {
		return 1
	}
	return absFloat(a-b) / max(absFloat(a), absFloat(b))
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func absFloat(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
