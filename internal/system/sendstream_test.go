package system

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"slices"
	"testing"
)

// sendStream builds Btrfs send streams for the tests.
type sendStream struct{ bytes.Buffer }

func newSendStream(version uint32) *sendStream {
	s := &sendStream{}
	s.WriteString(sendStreamMagic)
	binary.Write(s, binary.LittleEndian, version)
	return s
}

type sendTLV struct {
	kind  uint16
	value []byte
}

func (s *sendStream) cmd(kind uint16, attrs ...sendTLV) *sendStream {
	var payload bytes.Buffer
	for _, a := range attrs {
		binary.Write(&payload, binary.LittleEndian, a.kind)
		binary.Write(&payload, binary.LittleEndian, uint16(len(a.value)))
		payload.Write(a.value)
	}
	cmd := make([]byte, sendCmdHeaderSize, sendCmdHeaderSize+payload.Len())
	binary.LittleEndian.PutUint32(cmd[0:4], uint32(payload.Len()))
	binary.LittleEndian.PutUint16(cmd[4:6], kind)
	cmd = append(cmd, payload.Bytes()...)
	binary.LittleEndian.PutUint32(cmd[6:10], sendCRC(cmd))
	s.Write(cmd)
	return s
}

func sendU64(v uint64) []byte { return binary.LittleEndian.AppendUint64(nil, v) }

func sendPath(p string) sendTLV { return sendTLV{15, []byte(p)} }

func sendMode(m uint64) sendTLV { return sendTLV{sendAttrMode, sendU64(m)} }

var (
	uuidSent   = bytes.Repeat([]byte{0x11}, 16)
	uuidParent = bytes.Repeat([]byte{0x22}, 16)
	uuidOther  = bytes.Repeat([]byte{0x33}, 16)
)

// sentCommand is a command read back from a filtered stream.
type sentCommand struct {
	kind  uint16
	attrs []sendTLV
}

// readSendStream parses a stream, checking every checksum.
func readSendStream(t *testing.T, data []byte) []sentCommand {
	t.Helper()
	var out []sentCommand
	for len(data) > 0 {
		if !bytes.HasPrefix(data, []byte(sendStreamMagic)) {
			t.Fatalf("no stream header at %q", data[:min(len(data), 20)])
		}
		data = data[sendHeaderSize:]
		for len(data) > 0 && !bytes.HasPrefix(data, []byte(sendStreamMagic)) {
			size := binary.LittleEndian.Uint32(data[0:4])
			cmd := slices.Clone(data[:sendCmdHeaderSize+int(size)])
			sum := binary.LittleEndian.Uint32(cmd[6:10])
			binary.LittleEndian.PutUint32(cmd[6:10], 0)
			if sendCRC(cmd) != sum {
				t.Fatalf("command %d has a bad checksum", binary.LittleEndian.Uint16(cmd[4:6]))
			}
			c := sentCommand{kind: binary.LittleEndian.Uint16(cmd[4:6])}
			attrs := cmd[sendCmdHeaderSize:]
			for len(attrs) > 0 {
				k := binary.LittleEndian.Uint16(attrs[0:2])
				n := int(binary.LittleEndian.Uint16(attrs[2:4]))
				c.attrs = append(c.attrs, sendTLV{k, attrs[4 : 4+n]})
				attrs = attrs[4+n:]
			}
			out = append(out, c)
			data = data[sendCmdHeaderSize+int(size):]
		}
	}
	return out
}

func filtered(stream []byte, allow func(string) error) ([]byte, *SendStreamFilter, error) {
	f := FilterSendStream(bytes.NewReader(stream), allow)
	out, err := io.ReadAll(f)
	return out, f, err
}

func TestFilterSendStreamTakesPrivilegesAway(t *testing.T) {
	s := newSendStream(1).
		cmd(sendCmdSubvol, sendPath("blog"), sendTLV{sendAttrUUID, uuidSent}).
		cmd(3, sendPath("bin/tool")).
		cmd(15, sendPath("bin/tool"), sendTLV{18, sendU64(0)}, sendTLV{sendAttrData, []byte("#!/bin/sh\n")}).
		cmd(sendCmdChmod, sendPath("bin/tool"), sendMode(0o4755)).
		cmd(sendCmdChmod, sendPath("shared"), sendMode(0o2775), sendMode(0o6777)).
		cmd(sendCmdChmod, sendPath("plain"), sendMode(0o644)).
		cmd(sendCmdSetXattr, sendPath("bin/tool"), sendTLV{sendAttrXattrName, []byte("security.capability")}, sendTLV{14, []byte{1, 2}}).
		cmd(sendCmdSetXattr, sendPath("bin/tool"), sendTLV{sendAttrXattrName, []byte("user.note")}, sendTLV{sendAttrXattrName, []byte("trusted.overlay.opaque")}).
		cmd(sendCmdSetXattr, sendPath("bin/tool"), sendTLV{sendAttrXattrName, []byte("user.mime_type")}, sendTLV{14, []byte("text/plain")}).
		cmd(sendCmdFileattr, sendPath("bin/tool"), sendTLV{26, sendU64(0x10)}).
		cmd(sendCmdMknod, sendPath("fifo"), sendMode(0o010644|0o4000), sendTLV{8, sendU64(0)}).
		cmd(sendCmdClone, sendPath("copy"), sendTLV{sendAttrCloneUUID, uuidSent}, sendTLV{22, []byte("bin/tool")}).
		cmd(sendCmdEnd)
	out, f, err := filtered(s.Bytes(), nil)
	if err != nil {
		t.Fatal(err)
	}
	cmds := readSendStream(t, out)
	var kinds []uint16
	for _, c := range cmds {
		kinds = append(kinds, c.kind)
		for _, a := range c.attrs {
			if a.kind == sendAttrMode && binary.LittleEndian.Uint64(a.value)&modeSetID != 0 {
				t.Errorf("command %d still sets %o", c.kind, binary.LittleEndian.Uint64(a.value))
			}
		}
	}
	// Two xattrs and the inode flags are left out.
	want := []uint16{sendCmdSubvol, 3, 15, sendCmdChmod, sendCmdChmod, sendCmdChmod, sendCmdSetXattr, sendCmdMknod, sendCmdClone, sendCmdEnd}
	if !slices.Equal(kinds, want) {
		t.Errorf("commands = %v, want %v", kinds, want)
	}
	if got := binary.LittleEndian.Uint64(cmds[3].attrs[1].value); got != 0o755 {
		t.Errorf("mode = %o", got)
	}
	if got := binary.LittleEndian.Uint64(cmds[4].attrs[2].value); got != 0o777 {
		t.Errorf("a repeated mode = %o", got)
	}
	if f.Stripped != 4 || f.Dropped != 3 || f.Err() != nil {
		t.Errorf("stripped %d, dropped %d, %v", f.Stripped, f.Dropped, f.Err())
	}
	// Filtering again changes nothing.
	again, _, err := filtered(out, nil)
	if err != nil || !bytes.Equal(again, out) {
		t.Errorf("filtering twice: %v", err)
	}
}

func TestFilterSendStreamAsksAboutOtherSubvolumes(t *testing.T) {
	var asked []string
	allow := func(uuid string) error {
		asked = append(asked, uuid)
		if uuid == uuidText(uuidOther) {
			return ErrInvalid
		}
		return nil
	}
	incremental := newSendStream(1).
		cmd(sendCmdSnapshot, sendPath("blog-2"), sendTLV{sendAttrUUID, uuidSent}, sendTLV{sendAttrCloneUUID, uuidParent}).
		cmd(sendCmdClone, sendPath("a"), sendTLV{sendAttrCloneUUID, uuidParent}).
		cmd(sendCmdClone, sendPath("b"), sendTLV{sendAttrCloneUUID, uuidSent}).
		cmd(sendCmdEnd)
	if _, _, err := filtered(incremental.Bytes(), allow); err != nil {
		t.Fatal(err)
	}
	if want := []string{uuidText(uuidParent), uuidText(uuidParent)}; !slices.Equal(asked, want) {
		t.Errorf("asked about %v, want %v", asked, want)
	}
	if uuidText(uuidParent) != "22222222-2222-2222-2222-222222222222" {
		t.Errorf("uuid text = %s", uuidText(uuidParent))
	}
	for name, stream := range map[string][]byte{
		"a parent refused": newSendStream(1).cmd(sendCmdSnapshot, sendPath("x"), sendTLV{sendAttrUUID, uuidSent}, sendTLV{sendAttrCloneUUID, uuidOther}).Bytes(),
		"shared data from elsewhere": newSendStream(1).cmd(sendCmdSubvol, sendPath("x"), sendTLV{sendAttrUUID, uuidSent}).
			cmd(sendCmdClone, sendPath("a"), sendTLV{sendAttrCloneUUID, uuidOther}).Bytes(),
		"a second source in one command": newSendStream(1).cmd(sendCmdSubvol, sendPath("x"), sendTLV{sendAttrUUID, uuidSent}).
			cmd(sendCmdClone, sendPath("a"), sendTLV{sendAttrCloneUUID, uuidSent}, sendTLV{sendAttrCloneUUID, uuidOther}).Bytes(),
	} {
		if _, _, err := filtered(stream, allow); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Without a policy, a stream may only stand on its own.
	if _, _, err := filtered(incremental.Bytes(), nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("an incremental stream without a policy: %v", err)
	}
}

func TestFilterSendStreamRefusesBadStreams(t *testing.T) {
	good := newSendStream(1).cmd(sendCmdSubvol, sendPath("x"), sendTLV{sendAttrUUID, uuidSent}).cmd(sendCmdEnd).Bytes()
	corrupt := slices.Clone(good)
	corrupt[len(corrupt)-1] ^= 0xff
	truncated := good[:len(good)-3]
	wrongVersion := slices.Clone(good)
	wrongVersion[len(sendStreamMagic)] = 9
	for name, stream := range map[string][]byte{
		"empty":          {},
		"not a stream":   []byte("racksail-sim-send/1\n"),
		"wrong version":  wrongVersion,
		"bad checksum":   corrupt,
		"cut short":      truncated,
		"device node":    newSendStream(1).cmd(sendCmdMknod, sendPath("sda"), sendMode(0o060600), sendTLV{8, sendU64(0x800)}).Bytes(),
		"character node": newSendStream(1).cmd(sendCmdMknod, sendPath("mem"), sendMode(0o020600), sendTLV{8, sendU64(0x101)}).Bytes(),
		"mode missing":   newSendStream(1).cmd(sendCmdChmod, sendPath("a")).Bytes(),
		"mode too short": newSendStream(1).cmd(sendCmdChmod, sendPath("a"), sendTLV{sendAttrMode, []byte{1, 2}}).Bytes(),
		"huge command":   append(newSendStream(1).Bytes(), 0xff, 0xff, 0xff, 0xff, 3, 0, 0, 0, 0, 0),
		"broken attribute": func() []byte {
			s := newSendStream(1)
			cmd := []byte{6, 0, 0, 0, sendCmdChmod, 0, 0, 0, 0, 0, sendAttrMode, 0, 8, 0, 1, 2}
			binary.LittleEndian.PutUint32(cmd[6:10], sendCRC(cmd))
			s.Write(cmd)
			return s.Bytes()
		}(),
	} {
		if _, _, err := filtered(stream, nil); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Streams for several subvolumes follow one another.
	two := append(slices.Clone(good), good...)
	out, _, err := filtered(two, nil)
	if err != nil || !bytes.Equal(out, two) {
		t.Errorf("two streams: %v", err)
	}
}

func TestFilterSendStreamReadsVersionTwoData(t *testing.T) {
	// From version 2 the data of a write runs to the end of the command,
	// with no length, so bytes in it that look like attributes are data.
	s := newSendStream(2)
	data := append([]byte{sendAttrMode, 0, 8, 0}, sendU64(0o4755)...)
	payload := append([]byte{15, 0, 1, 0, 'f', sendAttrData, 0}, data...)
	cmd := make([]byte, sendCmdHeaderSize)
	binary.LittleEndian.PutUint32(cmd[0:4], uint32(len(payload)))
	binary.LittleEndian.PutUint16(cmd[4:6], sendCmdChmod)
	cmd = append(cmd, payload...)
	binary.LittleEndian.PutUint32(cmd[6:10], sendCRC(cmd))
	s.Write(cmd)
	if _, _, err := filtered(s.Bytes(), nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("a chmod whose mode hides in its data: %v", err)
	}
}

func FuzzFilterSendStream(f *testing.F) {
	f.Add(newSendStream(1).cmd(sendCmdSubvol, sendPath("x"), sendTLV{sendAttrUUID, uuidSent}).
		cmd(sendCmdChmod, sendPath("a"), sendMode(0o4755)).cmd(sendCmdEnd).Bytes())
	f.Add(newSendStream(2).cmd(sendCmdSetXattr, sendPath("a"), sendTLV{sendAttrXattrName, []byte("security.capability")}).Bytes())
	f.Fuzz(func(t *testing.T, data []byte) {
		out, _, err := filtered(data, func(string) error { return nil })
		if err != nil {
			return
		}
		// What passes keeps passing, unchanged, and sets no set-ID bit.
		again, f2, err := filtered(out, func(string) error { return nil })
		if err != nil || !bytes.Equal(again, out) || f2.Stripped != 0 || f2.Dropped != 0 {
			t.Fatalf("filtering the output again: %v", err)
		}
	})
}
