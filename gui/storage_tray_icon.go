//go:build windows

package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
)

func storageErrorIcon() []byte {
	img := image.NewNRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			dx, dy := x-16, y-16
			if dx*dx+dy*dy <= 225 {
				img.SetNRGBA(x, y, color.NRGBA{R: 200, G: 35, B: 35, A: 255})
			}
			if x >= 14 && x <= 17 && ((y >= 7 && y <= 19) || (y >= 23 && y <= 26)) {
				img.SetNRGBA(x, y, color.NRGBA{255, 255, 255, 255})
			}
		}
	}
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, img); err != nil {
		return nil
	}
	b := make([]byte, 22)
	binary.LittleEndian.PutUint16(b[2:], 1)
	binary.LittleEndian.PutUint16(b[4:], 1)
	b[6], b[7] = 32, 32
	binary.LittleEndian.PutUint16(b[10:], 1)
	binary.LittleEndian.PutUint16(b[12:], 32)
	binary.LittleEndian.PutUint32(b[14:], uint32(pngData.Len()))
	binary.LittleEndian.PutUint32(b[18:], 22)
	return append(b, pngData.Bytes()...)
}
