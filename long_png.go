package main

import (
	"bufio"
	"compress/zlib"
	"context"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/draw"
	"io"
)

func writeLongPNG(ctx context.Context, output io.Writer, width, height int, sources []exportSource) error {
	if _, err := io.WriteString(output, "\x89PNG\r\n\x1a\n"); err != nil {
		return err
	}
	var header [13]byte
	binary.BigEndian.PutUint32(header[:4], uint32(width))
	binary.BigEndian.PutUint32(header[4:8], uint32(height))
	header[8], header[9] = 8, 2 // Eight bits per RGB channel.
	if err := writePNGChunk(output, "IHDR", header[:]); err != nil {
		return err
	}

	// All IDAT chunks carry consecutive pieces of one zlib stream.
	chunks := bufio.NewWriterSize(pngImageData{output}, 64<<10)
	compressed, err := zlib.NewWriterLevel(chunks, zlib.BestSpeed)
	if err != nil {
		return err
	}
	row := make([]byte, 1+width*3)
	pixels := image.NewRGBA(image.Rect(0, 0, width, 1))
	xSamples := make([]sample, width)
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return err
		}
		img, _, err := image.Decode(source.file)
		if err != nil {
			return err
		}
		if source.crop != nil {
			img = source.crop.apply(img)
		}
		if err := writeScaledRows(ctx, compressed, img, source.height, row, pixels, xSamples); err != nil {
			return err
		}
	}
	if err := compressed.Close(); err != nil {
		return err
	}
	if err := chunks.Flush(); err != nil {
		return err
	}
	return writePNGChunk(output, "IEND", nil)
}

type pngImageData struct{ output io.Writer }

func (data pngImageData) Write(payload []byte) (int, error) {
	if err := writePNGChunk(data.output, "IDAT", payload); err != nil {
		return 0, err
	}
	return len(payload), nil
}

func writePNGChunk(output io.Writer, kind string, payload []byte) error {
	var header [8]byte
	binary.BigEndian.PutUint32(header[:4], uint32(len(payload)))
	copy(header[4:], kind)
	checksum := crc32.Update(crc32.ChecksumIEEE(header[4:]), crc32.IEEETable, payload)
	var footer [4]byte
	binary.BigEndian.PutUint32(footer[:], checksum)
	for _, part := range [][]byte{header[:], payload, footer[:]} {
		if _, err := output.Write(part); err != nil {
			return err
		}
	}
	return nil
}

func writeScaledRows(ctx context.Context, output io.Writer, src image.Image, height int, row []byte, pixels *image.RGBA, xSamples []sample) error {
	bounds := src.Bounds()
	width := len(xSamples)
	direct := width == bounds.Dx() && height == bounds.Dy()
	if !direct {
		for x := range xSamples {
			xSamples[x] = sampleAt(x, width, bounds.Min.X, bounds.Dx())
		}
	}
	for y := range height {
		if err := ctx.Err(); err != nil {
			return err
		}
		if direct {
			draw.Draw(pixels, pixels.Bounds(), image.White, image.Point{}, draw.Src)
			draw.Draw(pixels, pixels.Bounds(), src, image.Pt(bounds.Min.X, bounds.Min.Y+y), draw.Over)
			for x := range width {
				copy(row[1+x*3:1+x*3+3], pixels.Pix[x*4:x*4+3])
			}
		} else {
			ys := sampleAt(y, height, bounds.Min.Y, bounds.Dy())
			for x, xs := range xSamples {
				r0, g0, b0, a0 := src.At(xs.first, ys.first).RGBA()
				r1, g1, b1, a1 := src.At(xs.second, ys.first).RGBA()
				r2, g2, b2, a2 := src.At(xs.first, ys.second).RGBA()
				r3, g3, b3, a3 := src.At(xs.second, ys.second).RGBA()
				offset := 1 + x*3
				row[offset] = blend(r0+65535-a0, r1+65535-a1, r2+65535-a2, r3+65535-a3, xs.fraction, ys.fraction)
				row[offset+1] = blend(g0+65535-a0, g1+65535-a1, g2+65535-a2, g3+65535-a3, xs.fraction, ys.fraction)
				row[offset+2] = blend(b0+65535-a0, b1+65535-a1, b2+65535-a2, b3+65535-a3, xs.fraction, ys.fraction)
			}
		}
		// PNG's Sub filter uses the preceding pixel's corresponding channel.
		row[0] = 1
		for i := len(row) - 1; i > 3; i-- {
			row[i] -= row[i-3]
		}
		if _, err := output.Write(row); err != nil {
			return err
		}
	}
	return nil
}
