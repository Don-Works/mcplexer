package api

import (
	qrcode "github.com/skip2/go-qrcode"
)

// qrPNGSize is the side length in pixels of generated pairing QR codes.
// 256 px is large enough to scan reliably from a phone but small enough to
// transmit as a data URL without bloating REST responses.
const qrPNGSize = 256

// encodeQRPNG renders content as a PNG-encoded QR code at qrPNGSize px square.
// Uses medium error correction — good enough for short pairing payloads
// without bloating the bitmap.
func encodeQRPNG(content string) ([]byte, error) {
	return qrcode.Encode(content, qrcode.Medium, qrPNGSize)
}
