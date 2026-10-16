package system

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"strings"
)

// A Btrfs send stream is a list of commands that btrfs receive carries out
// as root: create this file, write these bytes, set this mode, start from a
// snapshot of that subvolume. Received streams come from wherever the panel
// says, so the Linux backend reads them through a SendStreamFilter on the
// way to btrfs receive. It takes away what every other way of bringing
// files onto the server refuses as well: set-user-ID and set-group-ID bits,
// device nodes, and extended attributes that grant capabilities or hold
// security labels. It also lets the caller decide about every other
// subvolume the stream names, since btrfs receive finds those anywhere on
// the filesystem and copies from them.

// Send stream framing, from the kernel's fs/btrfs/send.h.
const (
	sendStreamMagic   = "btrfs-stream\x00"
	sendHeaderSize    = len(sendStreamMagic) + 4
	sendCmdHeaderSize = 10
	// maxSendCommand bounds one command; the kernel's largest, an encoded
	// write, is far smaller.
	maxSendCommand = 32 << 20

	sendCmdSubvol   = 1
	sendCmdSnapshot = 2
	sendCmdMknod    = 5
	sendCmdSetXattr = 13
	sendCmdClone    = 16
	sendCmdChmod    = 18
	sendCmdEnd      = 21
	sendCmdFileattr = 24

	sendAttrUUID      = 1
	sendAttrMode      = 5
	sendAttrXattrName = 13
	sendAttrData      = 19
	sendAttrCloneUUID = 20
)

// File type and permission bits as send streams carry them.
const (
	modeTypeMask = 0o170000
	modeFIFO     = 0o010000
	modeSocket   = 0o140000
	modeSetID    = 0o6000
)

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// sendCRC is the checksum of a command: CRC-32C of its header, with the
// checksum field zero, and its attributes, seeded with zero and not
// inverted, as the kernel computes it.
func sendCRC(cmd []byte) uint32 {
	return ^crc32.Update(^uint32(0), castagnoli, cmd)
}

// SendStreamFilter filters a send stream as it is read.
type SendStreamFilter struct {
	r io.Reader
	// allow is asked about every subvolume the stream names other than the
	// one it brings: the parent of an incremental stream and the sources
	// of shared data. The stream fails unless it accepts.
	allow   func(uuid string) error
	header  bool
	version uint32
	current string
	out     bytes.Buffer
	err     error
	// Stripped counts the modes set-ID bits were taken from, and Dropped
	// the commands left out.
	Stripped, Dropped int
}

// FilterSendStream returns a reader of the send stream in r with set-ID
// bits cleared from every mode it sets, extended attributes in the
// security and trusted namespaces and inode flags left out, and device
// nodes refused. Every other subvolume the stream names goes to allow,
// which may refuse it. A stream that isn't well formed, or whose checksums
// don't match, fails to read.
func FilterSendStream(r io.Reader, allow func(uuid string) error) *SendStreamFilter {
	return &SendStreamFilter{r: r, allow: allow, header: true}
}

// Read implements io.Reader.
func (f *SendStreamFilter) Read(p []byte) (int, error) {
	for f.out.Len() == 0 && f.err == nil {
		f.err = f.next()
	}
	if f.out.Len() > 0 {
		return f.out.Read(p)
	}
	return 0, f.err
}

// Err returns what stopped the filter other than the end of the stream.
func (f *SendStreamFilter) Err() error {
	if errors.Is(f.err, io.EOF) {
		return nil
	}
	return f.err
}

func refuseStream(format string, args ...any) error {
	return fmt.Errorf("send stream: "+format+": %w", append(args, ErrInvalid)...)
}

// next reads the stream header or one command and queues what passes.
func (f *SendStreamFilter) next() error {
	if f.header {
		hdr := make([]byte, sendHeaderSize)
		n, err := io.ReadFull(f.r, hdr)
		switch {
		case n == 0 && errors.Is(err, io.EOF) && f.version != 0:
			// The end, after a stream ended with its END command.
			return io.EOF
		case err != nil:
			return refuseStream("no stream header")
		case string(hdr[:len(sendStreamMagic)]) != sendStreamMagic:
			return refuseStream("not a Btrfs send stream")
		}
		f.version = binary.LittleEndian.Uint32(hdr[len(sendStreamMagic):])
		if f.version < 1 || f.version > 3 {
			return refuseStream("version %d isn't supported", f.version)
		}
		f.header, f.current = false, ""
		f.out.Write(hdr)
		return nil
	}
	cmd := make([]byte, sendCmdHeaderSize)
	n, err := io.ReadFull(f.r, cmd)
	switch {
	case n == 0 && errors.Is(err, io.EOF):
		return io.EOF
	case err != nil:
		return refuseStream("a command is cut short")
	}
	size := binary.LittleEndian.Uint32(cmd[0:4])
	kind := binary.LittleEndian.Uint16(cmd[4:6])
	sum := binary.LittleEndian.Uint32(cmd[6:10])
	if size > maxSendCommand {
		return refuseStream("command %d is %d bytes long", kind, size)
	}
	cmd = append(cmd, make([]byte, size)...)
	if _, err := io.ReadFull(f.r, cmd[sendCmdHeaderSize:]); err != nil {
		return refuseStream("command %d is cut short", kind)
	}
	binary.LittleEndian.PutUint32(cmd[6:10], 0)
	if sendCRC(cmd) != sum {
		return refuseStream("command %d fails its checksum", kind)
	}
	keep, changed, err := f.inspect(kind, cmd[sendCmdHeaderSize:])
	if err != nil {
		return err
	}
	if !keep {
		f.Dropped++
		return nil
	}
	if changed {
		sum = sendCRC(cmd)
	}
	binary.LittleEndian.PutUint32(cmd[6:10], sum)
	f.out.Write(cmd)
	if kind == sendCmdEnd {
		// Another stream may follow, for the next subvolume.
		f.header = true
	}
	return nil
}

// inspect decides about one command: whether it passes, and whether its
// attributes were changed in place. btrfs receive keeps the last of
// attributes given twice, so every one is looked at.
func (f *SendStreamFilter) inspect(kind uint16, attrs []byte) (keep, changed bool, err error) {
	switch kind {
	case sendCmdSubvol, sendCmdSnapshot:
		err := f.eachAttr(attrs, func(t uint16, v []byte) error {
			switch {
			case t == sendAttrUUID:
				f.current = uuidText(v)
			case t == sendAttrCloneUUID && kind == sendCmdSnapshot:
				return f.refer(v, "the parent")
			}
			return nil
		})
		return err == nil, false, err
	case sendCmdClone:
		err := f.eachAttr(attrs, func(t uint16, v []byte) error {
			if t == sendAttrCloneUUID && uuidText(v) != f.current {
				return f.refer(v, "shared data")
			}
			return nil
		})
		return err == nil, false, err
	case sendCmdChmod, sendCmdMknod:
		modes := 0
		err := f.eachAttr(attrs, func(t uint16, v []byte) error {
			if t != sendAttrMode {
				return nil
			}
			if len(v) != 8 {
				return refuseStream("a mode isn't eight bytes long")
			}
			modes++
			m := binary.LittleEndian.Uint64(v)
			if kind == sendCmdMknod {
				if t := m & modeTypeMask; t != modeFIFO && t != modeSocket {
					return refuseStream("device nodes aren't received")
				}
			}
			if m&modeSetID != 0 {
				binary.LittleEndian.PutUint64(v, m&^modeSetID)
				f.Stripped++
				changed = true
			}
			return nil
		})
		if err == nil && modes == 0 {
			err = refuseStream("command %d has no mode", kind)
		}
		return err == nil, changed, err
	case sendCmdSetXattr:
		keep = true
		err := f.eachAttr(attrs, func(t uint16, v []byte) error {
			if t == sendAttrXattrName && (bytes.HasPrefix(v, []byte("security.")) || bytes.HasPrefix(v, []byte("trusted."))) {
				keep = false
			}
			return nil
		})
		return keep && err == nil, false, err
	case sendCmdFileattr:
		// Inode flags, such as immutable, which receive doesn't apply.
		return false, false, nil
	}
	return true, false, nil
}

// refer asks about a subvolume the stream names.
func (f *SendStreamFilter) refer(v []byte, what string) error {
	if len(v) != 16 {
		return refuseStream("a subvolume UUID isn't sixteen bytes long")
	}
	if f.allow == nil {
		return refuseStream("the stream takes %s from another subvolume", what)
	}
	if err := f.allow(uuidText(v)); err != nil {
		return fmt.Errorf("send stream: %s comes from subvolume %s: %w", what, uuidText(v), err)
	}
	return nil
}

// eachAttr calls fn with every attribute of a command as btrfs receive
// reads them: from version 2 on, the data attribute has no length and
// takes the rest of the command.
func (f *SendStreamFilter) eachAttr(attrs []byte, fn func(kind uint16, value []byte) error) error {
	for len(attrs) > 0 {
		if len(attrs) < 2 {
			return refuseStream("an attribute is cut short")
		}
		kind := binary.LittleEndian.Uint16(attrs[0:2])
		if f.version >= 2 && kind == sendAttrData {
			return fn(kind, attrs[2:])
		}
		if len(attrs) < 4 {
			return refuseStream("attribute %d is cut short", kind)
		}
		size := int(binary.LittleEndian.Uint16(attrs[2:4]))
		if len(attrs) < 4+size {
			return refuseStream("attribute %d is cut short", kind)
		}
		if err := fn(kind, attrs[4:4+size]); err != nil {
			return err
		}
		attrs = attrs[4+size:]
	}
	return nil
}

// uuidText writes a UUID the way btrfs subvolume list does.
func uuidText(v []byte) string {
	if len(v) != 16 {
		return ""
	}
	h := hex.EncodeToString(v)
	return strings.Join([]string{h[0:8], h[8:12], h[12:16], h[16:20], h[20:32]}, "-")
}
