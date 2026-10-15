package replaceresponse

import (
	"bytes"

	"golang.org/x/text/transform"
)

// literalTransformer emits replacements incrementally, so their size is not
// limited by the destination buffer or transform.Chain's intermediate buffers.
type literalTransformer struct {
	search, replace []byte
	pending         []byte
}

func (t *literalTransformer) Reset() { t.pending = nil }

func (t *literalTransformer) Transform(dst, src []byte, atEOF bool) (nDst, nSrc int, err error) {
	for {
		if len(t.pending) > 0 {
			n := copy(dst[nDst:], t.pending)
			nDst += n
			t.pending = t.pending[n:]
			if len(t.pending) > 0 {
				return nDst, nSrc, transform.ErrShortDst
			}
		}

		remaining := src[nSrc:]
		// Empty searches (including empty placeholder expansions) are a no-op,
		// matching the behavior of the previous literal transformer.
		index := -1
		if len(t.search) > 0 {
			index = bytes.Index(remaining, t.search)
		}
		if index < 0 {
			length := len(remaining)
			if !atEOF && len(t.search) > 0 {
				// Retain enough input for a match crossing the next chunk.
				length = max(0, length-len(t.search)+1)
			}
			n := copy(dst[nDst:], remaining[:length])
			nDst += n
			nSrc += n
			if n < length {
				return nDst, nSrc, transform.ErrShortDst
			}
			if !atEOF && len(t.search) > 0 {
				return nDst, nSrc, transform.ErrShortSrc
			}
			return nDst, nSrc, nil
		}

		n := copy(dst[nDst:], remaining[:index])
		nDst += n
		nSrc += n
		if n < index {
			return nDst, nSrc, transform.ErrShortDst
		}
		nSrc += len(t.search)
		t.pending = t.replace
	}
}
