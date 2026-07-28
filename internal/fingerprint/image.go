package fingerprint

import (
	"io"

	"photocull/internal/hashing"
	"photocull/internal/imageutil"
)

// imageExtractor is photocull's original behaviour, lifted out of the scanner
// without a line of it changing.
type imageExtractor struct{}

func (imageExtractor) Kind() Kind { return Photos }

func (imageExtractor) Extensions() []string { return imageutil.DefaultExtensions }

// Fingerprint decodes a photo and, when asked, reduces it to a perceptual hash.
//
// The shallow path stops after the image header. Dimensions are wanted for
// every scan — they decide which copy is worth keeping — and reading them costs
// almost nothing while the bytes are already flowing past the hasher. Decoding
// the whole image just to learn how big it is would make an exact-duplicates
// scan as slow as a similarity one for no benefit.
func (imageExtractor) Fingerprint(r io.Reader, in Input) Result {
	if !in.Deep {
		cfg, err := imageutil.DecodeConfig(r)
		if err != nil {
			return Result{Err: err}
		}
		return Result{Understood: true, Width: cfg.Width, Height: cfg.Height}
	}

	img, err := imageutil.Decode(r)
	if err != nil {
		return Result{Err: err}
	}

	bounds := img.Bounds()
	out := Result{Understood: true, Width: bounds.Dx(), Height: bounds.Dy()}

	// A photo whose pixels decoded but whose hash failed is still a usable
	// scan result: it has dimensions and a content hash, so it can be an exact
	// duplicate. Only the similarity match is lost.
	phash, err := hashing.Perceptual(img)
	if err != nil {
		out.Err = err
		return out
	}
	out.Fingerprint, out.HasFingerprint = phash, true
	return out
}
