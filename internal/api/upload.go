package api

import (
	"bytes"
	"errors"
	"fmt"
	"io"
)

// errUnsupportedImage is returned for uploads that are not a supported image type.
var errUnsupportedImage = errors.New("unsupported image type; send JPEG, PNG, WebP, TIFF or HEIC/HEIF")

// sniffImage returns the canonical extension for a supported image's leading bytes.
func sniffImage(head []byte) (string, error) {
	switch {
	case bytes.HasPrefix(head, []byte{0xFF, 0xD8, 0xFF}):
		return ".jpg", nil
	case bytes.HasPrefix(head, []byte("\x89PNG\r\n\x1a\n")):
		return ".png", nil
	case len(head) >= 12 && bytes.Equal(head[:4], []byte("RIFF")) && bytes.Equal(head[8:12], []byte("WEBP")):
		return ".webp", nil
	case bytes.HasPrefix(head, []byte("II*\x00")), bytes.HasPrefix(head, []byte("MM\x00*")):
		return ".tiff", nil
	case len(head) >= 12 && bytes.Equal(head[4:8], []byte("ftyp")):
		switch string(head[8:12]) {
		case "heic", "heix", "hevc", "heim", "heis", "mif1", "msf1":
			return ".heic", nil
		}
	}
	return "", errUnsupportedImage
}

// peekImage reads enough of r to identify it and returns a reader that
// replays those bytes followed by the rest of r.
func peekImage(r io.Reader) (io.Reader, string, error) {
	head := make([]byte, 16)
	n, err := io.ReadFull(r, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, "", fmt.Errorf("failed to read upload: %w", err)
	}
	head = head[:n]
	ext, err := sniffImage(head)
	if err != nil {
		return nil, "", err
	}
	return io.MultiReader(bytes.NewReader(head), r), ext, nil
}
