package main

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/jpeg"
	"io"
)

const maxPNGScanlines = 24_000_000*8 + 100_000

type boundedImageBuffer struct {
	bytes.Buffer
	limit int
}

func (b *boundedImageBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, io.ErrShortWrite
	}
	return b.Buffer.Write(p)
}

func optimizeImage(source []byte, img image.Image, format string) []byte {
	var optimized []byte
	switch format {
	case "png":
		optimized = recompressPNG(source)
	case "jpeg":
		optimized = recompressJPEG(source, img)
	}
	if len(optimized) != 0 && len(optimized) < len(source) {
		return optimized
	}
	return source
}

// Recompress the PNG's existing scanlines to preserve pixels, transparency, and
// ancillary chunks such as colour profiles. The source has already been decoded.
func recompressPNG(source []byte) []byte {
	var compressed bytes.Buffer
	first, end := 0, 0
	for offset := 8; offset+12 <= len(source); {
		size := binary.BigEndian.Uint32(source[offset:])
		if uint64(size) > uint64(len(source)-offset-12) {
			return nil
		}
		next := offset + int(size) + 12
		if string(source[offset+4:offset+8]) == "IDAT" {
			if first == 0 {
				first = offset
			} else if end != offset {
				return nil
			}
			compressed.Write(source[offset+8 : next-4])
			end = next
		}
		offset = next
	}
	if first == 0 || end-first <= 12 {
		return nil
	}
	in, err := zlib.NewReader(&compressed)
	if err != nil {
		return nil
	}
	defer in.Close()
	out := &boundedImageBuffer{limit: end - first - 13}
	encoder, err := zlib.NewWriterLevel(out, zlib.BestCompression)
	if err != nil {
		return nil
	}
	copied, err := io.Copy(encoder, io.LimitReader(in, maxPNGScanlines+1))
	closeErr := encoder.Close()
	if err != nil || closeErr != nil || copied > maxPNGScanlines {
		return nil
	}
	result := make([]byte, 0, first+out.Len()+12+len(source)-end)
	result = append(result, source[:first]...)
	result = binary.BigEndian.AppendUint32(result, uint32(out.Len()))
	checksumStart := len(result)
	result = append(result, "IDAT"...)
	result = append(result, out.Bytes()...)
	result = binary.BigEndian.AppendUint32(result, crc32.ChecksumIEEE(result[checksumStart:]))
	return append(result, source[end:]...)
}

func recompressJPEG(source []byte, img image.Image) []byte {
	// Go's encoder emits grayscale or 4:2:0 images. Keep the source when it
	// uses finer colour sampling or a colour model the encoder cannot retain.
	switch v := img.(type) {
	case *image.Gray:
	case *image.YCbCr:
		if v.SubsampleRatio != image.YCbCrSubsampleRatio420 {
			return nil
		}
	default:
		return nil
	}
	metadata, ok := jpegDisplayMetadata(source)
	if !ok {
		return nil
	}
	out := &boundedImageBuffer{limit: len(source) - 1 - len(metadata)}
	if out.limit < 2 || jpeg.Encode(out, img, &jpeg.Options{Quality: 90}) != nil {
		return nil
	}
	if len(metadata) == 0 {
		return out.Bytes()
	}
	result := make([]byte, 0, out.Len()+len(metadata))
	result = append(result, out.Bytes()[:2]...)
	result = append(result, metadata...)
	return append(result, out.Bytes()[2:]...)
}

// Keep orientation and colour metadata. Unknown application segments can refer
// to auxiliary images or to offsets in the original stream, so keep those
// source files intact instead of reattaching metadata we cannot interpret.
func jpegDisplayMetadata(source []byte) ([]byte, bool) {
	var metadata []byte
	for i := 2; i+3 < len(source) && source[i] == 0xff; {
		start := i
		for i < len(source) && source[i] == 0xff {
			i++
		}
		if i+2 >= len(source) {
			return nil, false
		}
		marker := source[i]
		if marker == 0xda {
			return metadata, true
		}
		length := int(binary.BigEndian.Uint16(source[i+1:]))
		end := i + 1 + length
		if length < 2 || end > len(source) {
			return nil, false
		}
		data := source[i+3 : end]
		keep := false
		switch marker {
		case 0xe0:
			keep = bytes.HasPrefix(data, []byte("JFIF\x00"))
		case 0xe1:
			keep = bytes.HasPrefix(data, []byte("Exif\x00\x00")) ||
				bytes.HasPrefix(data, []byte("http://ns.adobe.com/xap/1.0/\x00"))
		case 0xe2:
			keep = bytes.HasPrefix(data, []byte("ICC_PROFILE\x00"))
		case 0xee:
			keep = bytes.HasPrefix(data, []byte("Adobe")) && len(data) == 12 && data[11] == 1
		}
		if marker >= 0xe0 && marker <= 0xef && !keep {
			return nil, false
		}
		if keep {
			metadata = append(metadata, source[start:end]...)
		}
		i = end
	}
	return nil, false
}
