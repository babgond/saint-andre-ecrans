package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"testing"
	"time"
)

func fixtureImage() *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, 480, 270))
	for y := 0; y < 270; y++ {
		for x := 0; x < 480; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: byte(x / 2), G: byte(y / 2), B: byte((x + y) / 4), A: byte((x + y) % 256)})
		}
	}
	return img
}

func assertSamePixels(t *testing.T, before, after []byte) {
	t.Helper()
	original, _, err := image.Decode(bytes.NewReader(before))
	if err != nil {
		t.Fatal(err)
	}
	saved, _, err := image.Decode(bytes.NewReader(after))
	if err != nil {
		t.Fatal(err)
	}
	bounds := original.Bounds()
	if saved.Bounds() != bounds {
		t.Fatalf("dimensions modifiées : %v != %v", saved.Bounds(), bounds)
	}
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if color.NRGBA64Model.Convert(original.At(x, y)) != color.NRGBA64Model.Convert(saved.At(x, y)) {
				t.Fatalf("pixel modifié en (%d, %d)", x, y)
			}
		}
	}
}

func TestUploadStoresOneOptimizedPNG(t *testing.T) {
	a := testApp(t)
	var encoded bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.NoCompression}
	if err := enc.Encode(&encoded, fixtureImage()); err != nil {
		t.Fatal(err)
	}
	// Insert a valid gAMA chunk before the image data.
	raw := encoded.Bytes()
	i := bytes.Index(raw, []byte("IDAT")) - 4
	chunk := binary.BigEndian.AppendUint32(nil, 4)
	chunk = append(chunk, 'g', 'A', 'M', 'A')
	chunk = binary.BigEndian.AppendUint32(chunk, 45455)
	chunk = binary.BigEndian.AppendUint32(chunk, crc32.ChecksumIEEE(chunk[4:]))
	source := append(append(append([]byte{}, raw[:i]...), chunk...), raw[i:]...)

	w := call(t, a, "POST", "/api/admin/images", bytes.NewReader(source), true,
		map[string]string{"Content-Disposition": "attachment; filename=affiche.png"}, 201)
	var sizes struct{ Bytes, UploadBytes int }
	if err := json.Unmarshal(w.Body.Bytes(), &sizes); err != nil || sizes.Bytes >= len(source) || sizes.UploadBytes != len(source) {
		t.Fatalf("poids invalides : %+v, %v", sizes, err)
	}
	publications, err := a.publications(time.Now())
	if err != nil || len(publications) != 1 {
		t.Fatalf("visuels : %v, %v", publications, err)
	}
	file := publications[0].File
	w = call(t, a, "GET", "/media/"+file, nil, false, nil, 200)
	saved := w.Body.Bytes()
	if len(saved) != sizes.Bytes || int64(sizes.Bytes) != publications[0].Bytes || !bytes.Contains(saved, chunk) {
		t.Fatal("dimensions de stockage ou métadonnées PNG incohérentes")
	}
	assertSamePixels(t, source, saved)
	postImage(t, a, "images", "duplicata.png", source, 409)
	files, err := os.ReadDir(storagePath(a.data, originals, ""))
	if err != nil || len(files) != 1 || files[0].Name() != file {
		t.Fatalf("fichiers de diffusion stockés : %v, %v", files, err)
	}
}

func TestJPEGOptimization(t *testing.T) {
	var input bytes.Buffer
	if err := jpeg.Encode(&input, fixtureImage(), &jpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}
	raw := input.Bytes()
	// EXIF orientation 6 (rotate 90° clockwise).
	exif := []byte{
		0xff, 0xe1, 0, 34, 'E', 'x', 'i', 'f', 0, 0, 'I', 'I', 42, 0, 8, 0, 0, 0,
		1, 0, 0x12, 1, 3, 0, 1, 0, 0, 0, 6, 0, 0, 0, 0, 0, 0, 0,
	}
	source := append(append(append([]byte{}, raw[:2]...), exif...), raw[2:]...)
	img, err := jpeg.Decode(bytes.NewReader(source))
	if err != nil {
		t.Fatal(err)
	}
	saved := optimizeImage(source, img, "jpeg")
	if len(saved) >= len(source) || !bytes.Contains(saved, exif) {
		t.Fatal("le JPEG doit être allégé tout en gardant son orientation")
	}
	decoded, err := jpeg.Decode(bytes.NewReader(saved))
	if err != nil || decoded.Bounds() != img.Bounds() {
		t.Fatalf("JPEG recompressé invalide : %v", err)
	}

	input.Reset()
	if err := jpeg.Encode(&input, fixtureImage(), &jpeg.Options{Quality: 25}); err != nil {
		t.Fatal(err)
	}
	img, err = jpeg.Decode(bytes.NewReader(input.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if result := optimizeImage(input.Bytes(), img, "jpeg"); !bytes.Equal(result, input.Bytes()) {
		t.Fatal("un JPEG déjà plus léger doit être conservé")
	}
}
