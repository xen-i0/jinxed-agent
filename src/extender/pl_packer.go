package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"strings"
)

// Packer decodes the big-endian stream produced by the agent (the Windows
// beacon's C++ Packer writes big-endian integers and length prefixes), while
// PackArray below produces the little-endian stream consumed by the agent.
// This asymmetry is inherited from upstream Adaptix and must be preserved.
type Packer struct {
	buffer []byte
}

func CreatePacker(buffer []byte) *Packer {
	return &Packer{buffer: buffer}
}

func (p *Packer) Size() uint {
	return uint(len(p.buffer))
}

func (p *Packer) CheckPacker(types []string) bool {
	packerSize := p.Size()

	for _, t := range types {
		switch t {
		case "byte":
			if packerSize < 1 {
				return false
			}
			packerSize -= 1
		case "word":
			if packerSize < 2 {
				return false
			}
			packerSize -= 2
		case "int":
			if packerSize < 4 {
				return false
			}
			packerSize -= 4
		case "long":
			if packerSize < 8 {
				return false
			}
			packerSize -= 8
		case "array":
			if packerSize < 4 {
				return false
			}
			index := p.Size() - packerSize
			length := uint(binary.BigEndian.Uint32(p.buffer[index : index+4]))
			packerSize -= 4
			if packerSize < length {
				return false
			}
			packerSize -= length
		}
	}
	return true
}

func (p *Packer) ParseInt8() uint8 {
	if p.Size() < 1 {
		return 0
	}
	v := p.buffer[0]
	p.buffer = p.buffer[1:]
	return v
}

func (p *Packer) ParseInt16() uint16 {
	if p.Size() < 2 {
		return 0
	}
	v := binary.BigEndian.Uint16(p.buffer[:2])
	p.buffer = p.buffer[2:]
	return v
}

func (p *Packer) ParseInt32() uint {
	if p.Size() < 4 {
		return 0
	}
	v := binary.BigEndian.Uint32(p.buffer[:4])
	p.buffer = p.buffer[4:]
	return uint(v)
}

func (p *Packer) ParseInt64() uint64 {
	if p.Size() < 8 {
		return 0
	}
	v := binary.BigEndian.Uint64(p.buffer[:8])
	p.buffer = p.buffer[8:]
	return v
}

func (p *Packer) ParseBytes() []byte {
	size := p.ParseInt32()
	if p.Size() < size {
		return nil
	}
	b := p.buffer[:size]
	p.buffer = p.buffer[size:]
	return b
}

func (p *Packer) ParseString() string {
	return string(bytes.Trim(p.ParseBytes(), "\x00"))
}

// PackArray serialises a command payload for the agent. Strings are NUL
// terminated (the agent trims them), integers are little-endian and []byte is
// written verbatim without a length prefix - identical to the upstream beacon
// convention that both sides already implement.
func PackArray(array []interface{}) ([]byte, error) {
	var packData []byte

	for i := range array {
		switch v := array[i].(type) {

		case []byte:
			packData = append(packData, v...)

		case string:
			val := v
			if len(val) != 0 && !strings.HasSuffix(val, "\x00") {
				val += "\x00"
			}
			size := make([]byte, 4)
			binary.LittleEndian.PutUint32(size, uint32(len(val)))
			packData = append(packData, size...)
			packData = append(packData, []byte(val)...)

		case int:
			num := make([]byte, 4)
			binary.LittleEndian.PutUint32(num, uint32(v))
			packData = append(packData, num...)

		case uint32:
			num := make([]byte, 4)
			binary.LittleEndian.PutUint32(num, v)
			packData = append(packData, num...)

		case int64:
			num := make([]byte, 8)
			binary.LittleEndian.PutUint64(num, uint64(v))
			packData = append(packData, num...)

		case uint64:
			num := make([]byte, 8)
			binary.LittleEndian.PutUint64(num, v)
			packData = append(packData, num...)

		case bool:
			if v {
				packData = append(packData, 1)
			} else {
				packData = append(packData, 0)
			}

		default:
			return nil, errors.New("PackArray: unknown type")
		}
	}
	return packData, nil
}
