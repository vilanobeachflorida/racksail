package ber

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestIntegers(t *testing.T) {
	for _, c := range []struct {
		n    int64
		want []byte
	}{
		{0, []byte{Integer, 1, 0x00}},
		{127, []byte{Integer, 1, 0x7f}},
		{128, []byte{Integer, 2, 0x00, 0x80}},
		{256, []byte{Integer, 2, 0x01, 0x00}},
		{-1, []byte{Integer, 1, 0xff}},
		{-128, []byte{Integer, 1, 0x80}},
		{-129, []byte{Integer, 2, 0xff, 0x7f}},
		{1 << 40, []byte{Integer, 6, 0x01, 0, 0, 0, 0, 0}},
	} {
		got := Int(Integer, c.n)
		if !bytes.Equal(got, c.want) {
			t.Errorf("Int(%d) = % x, want % x", c.n, got, c.want)
		}
		content, rest, err := Expect(got, Integer)
		if err != nil || len(rest) != 0 {
			t.Fatalf("reading %d back: %v", c.n, err)
		}
		if n, err := DecodeInt(content); err != nil || n != c.n {
			t.Errorf("DecodeInt(% x) = %d, %v; want %d", content, n, err, c.n)
		}
	}
}

func TestLengths(t *testing.T) {
	for _, c := range []struct {
		size int
		head []byte
	}{
		{0, []byte{OctetString, 0}},
		{0x7f, []byte{OctetString, 0x7f}},
		{0x80, []byte{OctetString, 0x81, 0x80}},
		{0x100, []byte{OctetString, 0x82, 0x01, 0x00}},
		{70000, []byte{OctetString, 0x83, 0x01, 0x11, 0x70}},
	} {
		content := bytes.Repeat([]byte{'a'}, c.size)
		el := Element(OctetString, content)
		if !bytes.HasPrefix(el, c.head) || len(el) != len(c.head)+c.size {
			t.Errorf("%d bytes start % x, want % x", c.size, el[:min(len(el), 5)], c.head)
		}
		tag, got, rest, err := Next(append(el, 0x05, 0x00))
		if err != nil || tag != OctetString || !bytes.Equal(got, content) || !bytes.Equal(rest, []byte{0x05, 0x00}) {
			t.Errorf("reading %d bytes back: tag %#x, %d bytes, rest % x, %v", c.size, tag, len(got), rest, err)
		}
	}
}

func TestMalformedElements(t *testing.T) {
	for name, b := range map[string][]byte{
		"nothing":             {},
		"a tag alone":         {OctetString},
		"short content":       {OctetString, 5, 'a'},
		"indefinite length":   {OctetString, 0x80, 'a', 0, 0},
		"five length bytes":   {OctetString, 0x85, 1, 1, 1, 1, 1},
		"short length":        {OctetString, 0x82, 1},
		"a long tag":          {0x1f, 0x01, 0x00},
		"more than the limit": {OctetString, 0x84, 0x7f, 0xff, 0xff, 0xff},
	} {
		if _, _, _, err := Next(b); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, _, err := Expect(String(OctetString, "x"), Integer); !errors.Is(err, ErrMalformed) {
		t.Errorf("another tag than expected: %v", err)
	}
	for _, b := range [][]byte{nil, bytes.Repeat([]byte{1}, 9)} {
		if _, err := DecodeInt(b); !errors.Is(err, ErrMalformed) {
			t.Errorf("an integer of %d bytes: %v", len(b), err)
		}
	}
	if v, err := DecodeBool([]byte{0xff}); err != nil || !v {
		t.Errorf("true: %v %v", v, err)
	}
	if _, err := DecodeBool([]byte{1, 1}); !errors.Is(err, ErrMalformed) {
		t.Errorf("a boolean of two bytes: %v", err)
	}
}

func TestReadMessage(t *testing.T) {
	first := Join(Sequence, Int(Integer, 1), String(Application|2, ""))
	second := Join(Sequence, Int(Integer, 2), String(OctetString, string(bytes.Repeat([]byte{'x'}, 300))))
	r := bytes.NewReader(append(append([]byte(nil), first...), second...))
	for i, want := range [][]byte{first, second} {
		got, err := ReadMessage(r)
		if err != nil {
			t.Fatalf("message %d: %v", i+1, err)
		}
		_, content, _, _ := Next(want)
		if !bytes.Equal(got, content) {
			t.Errorf("message %d is % x, want % x", i+1, got, content)
		}
	}
	if _, err := ReadMessage(r); err != io.EOF {
		t.Errorf("after the last message: %v", err)
	}
	if _, err := ReadMessage(bytes.NewReader([]byte{OctetString, 0})); !errors.Is(err, ErrMalformed) {
		t.Errorf("a message that isn't a sequence: %v", err)
	}
	if _, err := ReadMessage(bytes.NewReader([]byte{Sequence, 4, 1, 2})); err != io.ErrUnexpectedEOF {
		t.Errorf("a message cut short: %v", err)
	}
	if _, err := ReadMessage(bytes.NewReader([]byte{Sequence, 0x84, 0x7f, 0xff, 0xff, 0xff})); !errors.Is(err, ErrMalformed) {
		t.Errorf("a message larger than the limit: %v", err)
	}
}

func FuzzNext(f *testing.F) {
	f.Add(Join(Sequence, Int(Integer, 7), String(OctetString, "cn=ada")))
	f.Add([]byte{OctetString, 0x82, 0x01})
	f.Fuzz(func(t *testing.T, b []byte) {
		tag, content, rest, err := Next(b)
		if err != nil {
			return
		}
		if len(content)+len(rest) >= len(b) || tag&0x1f == 0x1f {
			t.Fatalf("Next(% x) read %d bytes and left %d", b, len(content), len(rest))
		}
	})
}

func FuzzReadMessage(f *testing.F) {
	f.Add(Join(Sequence, Int(Integer, 1), Join(Application|Constructed|1, Int(Enumerated, 0))))
	f.Fuzz(func(t *testing.T, b []byte) {
		content, err := ReadMessage(bytes.NewReader(b))
		if err == nil && len(content) > len(b) {
			t.Fatalf("read %d bytes from %d", len(content), len(b))
		}
	})
}
