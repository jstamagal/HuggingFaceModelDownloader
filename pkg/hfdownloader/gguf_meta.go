// SPDX-License-Identifier: Apache-2.0

package hfdownloader

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

// GGUFMeta is the little bit of a GGUF header the views need.
type GGUFMeta struct {
	Architecture string
	Name         string
	FileType     int64 // general.file_type, -1 if absent
	Params       uint64
	SplitCount   int64 // split.count, 0 if not split
	SplitNo      int64
}

const ggufMagic = 0x46554747 // "GGUF" little-endian

// ReadGGUFMeta parses the header (kv + tensor infos) of a GGUF file without
// touching the tensor data.
func ReadGGUFMeta(path string) (*GGUFMeta, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := &ggufReader{r: bufio.NewReaderSize(f, 1<<20)}
	if r.u32() != ggufMagic {
		return nil, fmt.Errorf("%s: not a GGUF file", path)
	}
	ver := r.u32()
	if ver < 2 {
		return nil, fmt.Errorf("%s: GGUF v%d unsupported", path, ver)
	}
	nTensors := r.u64()
	nKV := r.u64()
	m := &GGUFMeta{FileType: -1}
	for i := uint64(0); i < nKV && r.err == nil; i++ {
		key := r.str()
		typ := r.u32()
		switch key {
		case "general.architecture":
			m.Architecture, _ = r.value(typ).(string)
		case "general.name":
			m.Name, _ = r.value(typ).(string)
		case "general.file_type":
			m.FileType = toInt64(r.value(typ))
		case "split.count":
			m.SplitCount = toInt64(r.value(typ))
		case "split.no":
			m.SplitNo = toInt64(r.value(typ))
		default:
			r.skip(typ)
		}
	}
	for i := uint64(0); i < nTensors && r.err == nil; i++ {
		r.str()
		nd := r.u32()
		n := uint64(1)
		for d := uint32(0); d < nd; d++ {
			n *= r.u64()
		}
		r.u32() // type
		r.u64() // offset
		m.Params += n
	}
	if r.err != nil {
		return nil, fmt.Errorf("%s: %w", path, r.err)
	}
	return m, nil
}

func toInt64(v any) int64 {
	switch x := v.(type) {
	case uint8:
		return int64(x)
	case int8:
		return int64(x)
	case uint16:
		return int64(x)
	case int16:
		return int64(x)
	case uint32:
		return int64(x)
	case int32:
		return int64(x)
	case uint64:
		return int64(x)
	case int64:
		return x
	}
	return -1
}

type ggufReader struct {
	r   *bufio.Reader
	err error
}

func (g *ggufReader) read(n int) []byte {
	if g.err != nil {
		return make([]byte, n)
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(g.r, b); err != nil {
		g.err = err
	}
	return b
}

func (g *ggufReader) discard(n uint64) {
	if g.err != nil {
		return
	}
	if _, err := g.r.Discard(int(n)); err != nil {
		g.err = err
	}
}

func (g *ggufReader) u32() uint32 { return binary.LittleEndian.Uint32(g.read(4)) }
func (g *ggufReader) u64() uint64 { return binary.LittleEndian.Uint64(g.read(8)) }

func (g *ggufReader) str() string {
	n := g.u64()
	if n > 1<<24 {
		g.err = errors.New("gguf: absurd string length")
		return ""
	}
	return string(g.read(int(n)))
}

// GGUF value types.
var ggufFixedSize = map[uint32]uint64{0: 1, 1: 1, 2: 2, 3: 2, 4: 4, 5: 4, 6: 4, 7: 1, 10: 8, 11: 8, 12: 8}

func (g *ggufReader) value(typ uint32) any {
	switch typ {
	case 0:
		return g.read(1)[0]
	case 1:
		return int8(g.read(1)[0])
	case 2:
		return binary.LittleEndian.Uint16(g.read(2))
	case 3:
		return int16(binary.LittleEndian.Uint16(g.read(2)))
	case 4:
		return g.u32()
	case 5:
		return int32(g.u32())
	case 10:
		return g.u64()
	case 11:
		return int64(g.u64())
	case 8:
		return g.str()
	}
	g.skip(typ)
	return nil
}

func (g *ggufReader) skip(typ uint32) {
	if sz, ok := ggufFixedSize[typ]; ok {
		g.discard(sz)
		return
	}
	switch typ {
	case 8:
		g.discard(g.u64())
	case 9:
		et := g.u32()
		n := g.u64()
		if sz, ok := ggufFixedSize[et]; ok {
			g.discard(sz * n)
			return
		}
		for i := uint64(0); i < n && g.err == nil; i++ {
			g.skip(et)
		}
	default:
		g.err = fmt.Errorf("gguf: unknown value type %d", typ)
	}
}

// humanParams mirrors ollama's format.HumanNumber ("1.94B", "27B", "630M").
func humanParams(n uint64) string {
	f := float64(n)
	unit := ""
	switch {
	case f >= 1e12:
		f, unit = f/1e12, "T"
	case f >= 1e9:
		f, unit = f/1e9, "B"
	case f >= 1e6:
		f, unit = f/1e6, "M"
	case f >= 1e3:
		f, unit = f/1e3, "K"
	}
	s := fmt.Sprintf("%.2f", f)
	switch {
	case f >= 100:
		s = fmt.Sprintf("%.0f", f)
	case f >= 10:
		s = fmt.Sprintf("%.1f", f)
	}
	for len(s) > 1 && (s[len(s)-1] == '0' || s[len(s)-1] == '.') && containsDot(s) {
		last := s[len(s)-1]
		s = s[:len(s)-1]
		if last == '.' {
			break
		}
	}
	return s + unit
}

func containsDot(s string) bool {
	for i := range s {
		if s[i] == '.' {
			return true
		}
	}
	return false
}
