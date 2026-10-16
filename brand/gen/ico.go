package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// ICO packs square PNG images into a Windows icon file. Every browser and
// Windows since Vista read PNG entries, which keep the file small.
func ICO(images ...*Canvas) []byte {
	var b bytes.Buffer
	le := binary.LittleEndian
	header := make([]byte, 6)
	le.PutUint16(header[2:], 1)
	le.PutUint16(header[4:], uint16(len(images)))
	b.Write(header)
	pngs := make([][]byte, len(images))
	offset := 6 + 16*len(images)
	for i, img := range images {
		if img.W != img.H || img.W > 256 {
			panic(fmt.Sprintf("icon images must be square and at most 256 pixels, not %dx%d", img.W, img.H))
		}
		pngs[i] = img.PNG()
		entry := make([]byte, 16)
		// A size of 256 is written as 0.
		entry[0] = byte(img.W)
		entry[1] = byte(img.H)
		le.PutUint16(entry[4:], 1)
		le.PutUint16(entry[6:], 32)
		le.PutUint32(entry[8:], uint32(len(pngs[i])))
		le.PutUint32(entry[12:], uint32(offset))
		offset += len(pngs[i])
		b.Write(entry)
	}
	for _, p := range pngs {
		b.Write(p)
	}
	return b.Bytes()
}

// icoEntries splits an icon file into its images, for checking it.
func icoEntries(data []byte) ([][]byte, error) {
	le := binary.LittleEndian
	if len(data) < 6 || le.Uint16(data[2:]) != 1 {
		return nil, fmt.Errorf("not an icon file")
	}
	n := int(le.Uint16(data[4:]))
	if len(data) < 6+16*n {
		return nil, fmt.Errorf("the icon directory is cut short")
	}
	out := make([][]byte, n)
	for i := range n {
		e := data[6+16*i:]
		size, off := int(le.Uint32(e[8:])), int(le.Uint32(e[12:]))
		if off+size > len(data) {
			return nil, fmt.Errorf("icon image %d runs past the end of the file", i)
		}
		out[i] = data[off : off+size]
	}
	return out, nil
}
