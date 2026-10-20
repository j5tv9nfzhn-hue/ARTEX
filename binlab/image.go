// Package binlab implements dependency-free binary analysis primitives.
//
// It links only the Go standard library: container parsing, address-space
// mapping, entropy metrics, triage indicators and algorithm signature
// scanning. It never executes, attaches to, or modifies a sample.
package binlab

import (
	"bytes"
	"crypto/sha256"
	"debug/elf"
	"debug/macho"
	"debug/pe"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
)

const (
	// MaxImageBytes bounds every read so a hostile sample cannot exhaust memory.
	MaxImageBytes int64 = 512 << 20
	// maxEntropyBytes bounds the CPU spent on per-section statistics.
	maxEntropyBytes = 8 << 20
	// maxCStringBytes bounds a single name read out of a header directory.
	maxCStringBytes = 256
)

// Section is one mapped region of the container.
//
// RVA is the address used by the image itself. For PE that is a true
// relative virtual address; for ELF and Mach-O it is the link-time virtual
// address. AddressKind on the parent Image says which one applies.
type Section struct {
	Name       string  `json:"name"`
	Index      int     `json:"index"`
	RVA        uint64  `json:"rva"`
	VSize      uint64  `json:"vsize"`
	RawOffset  int64   `json:"raw_offset"`
	RawSize    uint64  `json:"raw_size"`
	Perm       string  `json:"perm"`
	Readable   bool    `json:"readable"`
	Writable   bool    `json:"writable"`
	Executable bool    `json:"executable"`
	Entropy    float64 `json:"entropy"`
	ZeroRatio  float64 `json:"zero_ratio"`
}

// span reports how much address space the section claims. A zero VirtualSize
// is common in object files, so fall back to the raw size.
func (s Section) span() uint64 {
	if s.VSize > 0 {
		return s.VSize
	}
	return s.RawSize
}

// Import is one library and the symbols resolved from it.
type Import struct {
	Library string   `json:"library"`
	Symbols []string `json:"symbols"`
}

// Export is one named export.
type Export struct {
	Name    string `json:"name"`
	RVA     uint64 `json:"rva"`
	Ordinal uint32 `json:"ordinal"`
}

// Image is a parsed container. The raw bytes stay unexported so that a JSON
// view of an Image never carries the whole sample.
type Image struct {
	Path          string    `json:"path"`
	SHA256        string    `json:"sha256"`
	Format        string    `json:"format"`
	Arch          string    `json:"arch"`
	Bits          int       `json:"bits"`
	AddressKind   string    `json:"address_kind"`
	ImageBase     uint64    `json:"image_base"`
	EntryRVA      uint64    `json:"entry_rva"`
	Size          int64     `json:"size_bytes"`
	Sections      []Section `json:"sections"`
	Imports       []Import  `json:"imports"`
	Exports       []Export  `json:"exports"`
	OverlayOffset int64     `json:"overlay_offset"`
	OverlaySize   int64     `json:"overlay_size"`
	Notes         []string  `json:"notes"`

	data []byte
}

// Load reads and parses a container from disk. The whole file is held in
// memory, bounded by MaxImageBytes, because every later mode addresses it by
// offset and re-reading would let the sample change under the analysis.
func Load(path string) (*Image, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("binlab: sample must be a regular file")
	}
	if info.Size() > MaxImageBytes {
		return nil, fmt.Errorf("binlab: sample exceeds %d bytes", MaxImageBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return LoadBytes(path, data)
}

// LoadBytes parses an in-memory image. It is exported so tests and the dump
// pipeline can analyse buffers that never touched disk.
func LoadBytes(path string, data []byte) (*Image, error) {
	if int64(len(data)) > MaxImageBytes {
		return nil, fmt.Errorf("binlab: image exceeds %d bytes", MaxImageBytes)
	}
	sum := sha256.Sum256(data)
	img := &Image{
		Path:          path,
		SHA256:        hex.EncodeToString(sum[:]),
		Size:          int64(len(data)),
		OverlayOffset: -1,
		data:          data,
	}
	if err := img.parse(); err != nil {
		return nil, err
	}
	return img, nil
}

func (img *Image) parse() error {
	data := img.data
	switch {
	case len(data) >= 2 && data[0] == 'M' && data[1] == 'Z':
		return img.parsePE()
	case len(data) >= 4 && data[0] == 0x7f && data[1] == 'E' && data[2] == 'L' && data[3] == 'F':
		return img.parseELF()
	case isMachO(data):
		return img.parseMachO()
	}
	return fmt.Errorf("binlab: unsupported container (%d bytes)", len(data))
}

func isMachO(data []byte) bool {
	if len(data) < 4 {
		return false
	}
	switch binary.BigEndian.Uint32(data[:4]) {
	case 0xfeedface, 0xfeedfacf, 0xcefaedfe, 0xcffaedfe:
		return true
	}
	return false
}

func (img *Image) parsePE() error {
	f, err := pe.NewFile(bytes.NewReader(img.data))
	if err != nil {
		return fmt.Errorf("binlab: PE parse: %w", err)
	}
	defer f.Close()

	img.Format = "pe"
	img.AddressKind = "rva"
	arch, bits := peArch(f.Machine)
	img.Arch = arch
	img.Bits = bits

	var impRVA, expRVA uint64
	switch oh := f.OptionalHeader.(type) {
	case *pe.OptionalHeader64:
		img.ImageBase = oh.ImageBase
		img.EntryRVA = uint64(oh.AddressOfEntryPoint)
		impRVA = uint64(oh.DataDirectory[1].VirtualAddress)
		expRVA = uint64(oh.DataDirectory[0].VirtualAddress)
		img.Bits = 64
	case *pe.OptionalHeader32:
		img.ImageBase = uint64(oh.ImageBase)
		img.EntryRVA = uint64(oh.AddressOfEntryPoint)
		impRVA = uint64(oh.DataDirectory[1].VirtualAddress)
		expRVA = uint64(oh.DataDirectory[0].VirtualAddress)
		img.Bits = 32
	default:
		return fmt.Errorf("binlab: PE without a usable optional header")
	}

	for i, s := range f.Sections {
		sec := Section{
			Name:      strings.TrimRight(s.Name, "\x00"),
			Index:     i,
			RVA:       uint64(s.VirtualAddress),
			VSize:     uint64(s.VirtualSize),
			RawOffset: int64(s.Offset),
			RawSize:   uint64(s.Size),
			// IMAGE_SCN_MEM_READ | _WRITE | _EXECUTE
			Readable:   s.Characteristics&0x40000000 != 0,
			Writable:   s.Characteristics&0x80000000 != 0,
			Executable: s.Characteristics&0x20000000 != 0,
		}
		sec.Perm = permString(sec.Readable, sec.Writable, sec.Executable)
		if body, ok := sliceRaw(img.data, sec.RawOffset, sec.RawSize); ok {
			sec.Entropy = Shannon(body)
			sec.ZeroRatio = zeroRatio(body)
		}
		img.Sections = append(img.Sections, sec)
	}

	img.OverlayOffset, img.OverlaySize = peOverlay(img.data, img.Sections)
	img.Imports = img.peImports(impRVA)
	img.Exports = img.peExports(expRVA)
	return nil
}

// peImports walks the import directory by hand: debug/pe exposes symbols but
// drops the DLL/symbol pairing and the per-library grouping that triage needs.
func (img *Image) peImports(dirRVA uint64) []Import {
	if dirRVA == 0 {
		return nil
	}
	dirOff, ok := img.RVAtoOffset(dirRVA)
	if !ok {
		return nil
	}
	thunkSize := int64(4)
	if img.Bits == 64 {
		thunkSize = 8
	}
	const (
		maxLibraries = 512
		maxThunks    = 4096
		descriptor   = 20
	)
	out := make([]Import, 0, 16)
	for i := 0; i < maxLibraries; i++ {
		p := dirOff + int64(i*descriptor)
		if p < 0 || p+int64(descriptor) > int64(len(img.data)) {
			break
		}
		d := img.data[p : p+descriptor]
		originalFirstThunk := binary.LittleEndian.Uint32(d[0:4])
		nameRVA := binary.LittleEndian.Uint32(d[12:16])
		firstThunk := binary.LittleEndian.Uint32(d[16:20])
		if nameRVA == 0 && firstThunk == 0 {
			break
		}
		lib := img.cstringAtRVA(uint64(nameRVA))
		if lib == "" {
			continue
		}
		imp := Import{Library: lib}
		thunkRVA := uint64(originalFirstThunk)
		if thunkRVA == 0 {
			thunkRVA = uint64(firstThunk)
		}
		if thunkOff, ok := img.RVAtoOffset(thunkRVA); ok {
			for j := 0; j < maxThunks; j++ {
				q := thunkOff + int64(j)*thunkSize
				if q < 0 || q+thunkSize > int64(len(img.data)) {
					break
				}
				if thunkSize == 8 {
					v := binary.LittleEndian.Uint64(img.data[q : q+8])
					if v == 0 {
						break
					}
					if v&(uint64(1)<<63) != 0 {
						imp.Symbols = append(imp.Symbols, fmt.Sprintf("#%d", v&0xffff))
						continue
					}
					if name := img.cstringAtRVA((v & 0x7fffffff) + 2); name != "" {
						imp.Symbols = append(imp.Symbols, name)
					}
					continue
				}
				u := binary.LittleEndian.Uint32(img.data[q : q+4])
				if u == 0 {
					break
				}
				if u&(uint32(1)<<31) != 0 {
					imp.Symbols = append(imp.Symbols, fmt.Sprintf("#%d", u&0xffff))
					continue
				}
				if name := img.cstringAtRVA(uint64(u) + 2); name != "" {
					imp.Symbols = append(imp.Symbols, name)
				}
			}
		}
		out = append(out, imp)
	}
	sort.SliceStable(out, func(a, b int) bool {
		return strings.ToLower(out[a].Library) < strings.ToLower(out[b].Library)
	})
	return out
}

// peExports walks the export directory so callers get RVAs, not just names.
func (img *Image) peExports(dirRVA uint64) []Export {
	if dirRVA == 0 {
		return nil
	}
	off, ok := img.RVAtoOffset(dirRVA)
	if !ok || off+40 > int64(len(img.data)) {
		return nil
	}
	d := img.data[off : off+40]
	base := binary.LittleEndian.Uint32(d[16:20])
	numNames := binary.LittleEndian.Uint32(d[24:28])
	funcsRVA := uint64(binary.LittleEndian.Uint32(d[28:32]))
	namesRVA := uint64(binary.LittleEndian.Uint32(d[32:36]))
	ordsRVA := uint64(binary.LittleEndian.Uint32(d[36:40]))
	if numNames == 0 || numNames > 65535 {
		return nil
	}
	funcOff, ok := img.RVAtoOffset(funcsRVA)
	if !ok {
		return nil
	}
	nameOff, ok := img.RVAtoOffset(namesRVA)
	if !ok {
		return nil
	}
	ordOff, ok := img.RVAtoOffset(ordsRVA)
	if !ok {
		return nil
	}
	out := make([]Export, 0, numNames)
	for i := uint32(0); i < numNames; i++ {
		np := nameOff + int64(i)*4
		op := ordOff + int64(i)*2
		if np+4 > int64(len(img.data)) || op+2 > int64(len(img.data)) {
			break
		}
		name := img.cstringAtRVA(uint64(binary.LittleEndian.Uint32(img.data[np : np+4])))
		if name == "" {
			continue
		}
		ord := uint32(binary.LittleEndian.Uint16(img.data[op : op+2]))
		var fnRVA uint64
		fp := funcOff + int64(ord)*4
		if fp+4 <= int64(len(img.data)) {
			fnRVA = uint64(binary.LittleEndian.Uint32(img.data[fp : fp+4]))
		}
		out = append(out, Export{Name: name, RVA: fnRVA, Ordinal: base + ord})
	}
	return out
}

func (img *Image) parseELF() error {
	f, err := elf.NewFile(bytes.NewReader(img.data))
	if err != nil {
		return fmt.Errorf("binlab: ELF parse: %w", err)
	}
	defer f.Close()

	img.Format = "elf"
	img.AddressKind = "va"
	img.Arch = elfArch(f.Machine)
	img.Bits = int(f.Class) * 32
	img.EntryRVA = f.Entry

	for i, s := range f.Sections {
		sec := Section{
			Name:       s.Name,
			Index:      i,
			RVA:        s.Addr,
			VSize:      s.Size,
			RawOffset:  int64(s.Offset),
			RawSize:    s.Size,
			Readable:   true,
			Writable:   s.Flags&elf.SHF_WRITE != 0,
			Executable: s.Flags&elf.SHF_EXECINSTR != 0,
		}
		sec.Perm = permString(sec.Readable, sec.Writable, sec.Executable)
		if body, ok := sliceRaw(img.data, sec.RawOffset, sec.RawSize); ok {
			sec.Entropy = Shannon(body)
			sec.ZeroRatio = zeroRatio(body)
		}
		img.Sections = append(img.Sections, sec)
	}

	// debug/elf returns each imported symbol with its owning library, so the
	// grouping mirrors the PE path instead of collapsing everything together.
	if syms, err := f.ImportedSymbols(); err == nil && len(syms) > 0 {
		byLib := make(map[string][]string, 16)
		var order []string
		for _, s := range syms {
			lib := s.Library
			if lib == "" {
				lib = "(dynamic)"
			}
			if _, seen := byLib[lib]; !seen {
				order = append(order, lib)
			}
			name := s.Name
			if s.Version != "" {
				name += "@" + s.Version
			}
			byLib[lib] = append(byLib[lib], name)
		}
		for _, lib := range order {
			img.Imports = append(img.Imports, Import{Library: lib, Symbols: byLib[lib]})
		}
	}
	if libs, err := f.ImportedLibraries(); err == nil && len(libs) > 0 {
		img.Notes = append(img.Notes, "needed: "+strings.Join(libs, ", "))
	}
	return nil
}

func (img *Image) parseMachO() error {
	f, err := macho.NewFile(bytes.NewReader(img.data))
	if err != nil {
		return fmt.Errorf("binlab: Mach-O parse: %w", err)
	}
	defer f.Close()

	img.Format = "macho"
	img.AddressKind = "va"
	img.Arch = machoArch(f.Cpu)
	img.Bits = 32
	if f.Magic == macho.Magic64 {
		img.Bits = 64
	}

	for i, s := range f.Sections {
		sec := Section{
			Name:      s.Name,
			Index:     i,
			RVA:       s.Addr,
			VSize:     s.Size,
			RawOffset: int64(s.Offset),
			RawSize:   s.Size,
			Readable:  true,
		}
		// Section headers do not carry permissions; the segment is the hint.
		if strings.HasPrefix(s.Seg, "__TEXT") {
			sec.Executable = true
		}
		if strings.HasPrefix(s.Seg, "__DATA") {
			sec.Writable = true
		}
		sec.Perm = permString(sec.Readable, sec.Writable, sec.Executable)
		if body, ok := sliceRaw(img.data, sec.RawOffset, sec.RawSize); ok {
			sec.Entropy = Shannon(body)
			sec.ZeroRatio = zeroRatio(body)
		}
		img.Sections = append(img.Sections, sec)
	}

	if libs, err := f.ImportedLibraries(); err == nil {
		for _, l := range libs {
			img.Imports = append(img.Imports, Import{Library: l})
		}
	}
	return nil
}

// RVAtoOffset maps an address in the image coordinate system to a file
// offset. Bytes that live only in the zero-filled tail of a section have no
// file offset and report false.
func (img *Image) RVAtoOffset(rva uint64) (int64, bool) {
	for _, s := range img.Sections {
		span := s.span()
		if span == 0 {
			continue
		}
		if rva < s.RVA || rva >= s.RVA+span {
			continue
		}
		delta := rva - s.RVA
		if delta >= s.RawSize {
			return 0, false
		}
		off := s.RawOffset + int64(delta)
		if off < 0 || off >= int64(len(img.data)) {
			return 0, false
		}
		return off, true
	}
	return 0, false
}

// SectionAt returns the section covering an address.
func (img *Image) SectionAt(rva uint64) (Section, bool) {
	for _, s := range img.Sections {
		span := s.span()
		if span == 0 {
			continue
		}
		if rva >= s.RVA && rva < s.RVA+span {
			return s, true
		}
	}
	return Section{}, false
}

// BytesAtRVA returns up to n bytes starting at an address, clamped to the end
// of the file.
func (img *Image) BytesAtRVA(rva uint64, n int) ([]byte, bool) {
	if n <= 0 {
		return nil, false
	}
	off, ok := img.RVAtoOffset(rva)
	if !ok {
		return nil, false
	}
	end := off + int64(n)
	if end > int64(len(img.data)) {
		end = int64(len(img.data))
	}
	return img.data[off:end], true
}

// Raw returns the backing bytes without copying. Callers must not mutate it.
func (img *Image) Raw() []byte { return img.data }

func (img *Image) cstringAtRVA(rva uint64) string {
	off, ok := img.RVAtoOffset(rva)
	if !ok || off >= int64(len(img.data)) {
		return ""
	}
	end := off
	limit := off + maxCStringBytes
	if limit > int64(len(img.data)) {
		limit = int64(len(img.data))
	}
	for end < limit && img.data[end] != 0 {
		end++
	}
	return string(img.data[off:end])
}

func peOverlay(data []byte, secs []Section) (int64, int64) {
	var end int64
	for _, s := range secs {
		e := s.RawOffset + int64(s.RawSize)
		if e > end {
			end = e
		}
	}
	if end <= 0 || end >= int64(len(data)) {
		return end, 0
	}
	return end, int64(len(data)) - end
}

// Shannon returns the byte entropy of b in bits per byte, sampled from the
// first maxEntropyBytes to keep the cost bounded.
func Shannon(b []byte) float64 {
	if len(b) == 0 {
		return 0
	}
	if len(b) > maxEntropyBytes {
		b = b[:maxEntropyBytes]
	}
	var counts [256]int
	for _, c := range b {
		counts[c]++
	}
	n := float64(len(b))
	var h float64
	for _, c := range counts {
		if c == 0 {
			continue
		}
		p := float64(c) / n
		h -= p * math.Log2(p)
	}
	return math.Round(h*1000) / 1000
}

func zeroRatio(b []byte) float64 {
	if len(b) == 0 {
		return 0
	}
	if len(b) > maxEntropyBytes {
		b = b[:maxEntropyBytes]
	}
	zeros := 0
	for _, c := range b {
		if c == 0 {
			zeros++
		}
	}
	return math.Round(float64(zeros)/float64(len(b))*10000) / 10000
}

func permString(r, w, x bool) string {
	b := []byte("---")
	if r {
		b[0] = 'r'
	}
	if w {
		b[1] = 'w'
	}
	if x {
		b[2] = 'x'
	}
	return string(b)
}

func sliceRaw(data []byte, off int64, size uint64) ([]byte, bool) {
	if off < 0 || off >= int64(len(data)) || size == 0 {
		return nil, false
	}
	end := off + int64(size)
	if end > int64(len(data)) {
		end = int64(len(data))
	}
	return data[off:end], true
}

func peArch(m uint16) (string, int) {
	switch m {
	case 0x8664:
		return "x86_64", 64
	case 0x14c:
		return "x86", 32
	case 0xaa64:
		return "arm64", 64
	case 0x1c4:
		return "arm", 32
	}
	return fmt.Sprintf("machine_0x%x", m), 0
}

func elfArch(m elf.Machine) string {
	switch m {
	case elf.EM_X86_64:
		return "x86_64"
	case elf.EM_386:
		return "x86"
	case elf.EM_AARCH64:
		return "arm64"
	case elf.EM_ARM:
		return "arm"
	case elf.EM_RISCV:
		return "riscv"
	}
	return strings.ToLower(m.String())
}

func machoArch(c macho.Cpu) string {
	switch c {
	case macho.CpuAmd64:
		return "x86_64"
	case macho.Cpu386:
		return "x86"
	case macho.CpuArm64:
		return "arm64"
	case macho.CpuArm:
		return "arm"
	case macho.CpuPpc64:
		return "ppc64"
	case macho.CpuPpc:
		return "ppc"
	}
	return fmt.Sprintf("cpu_0x%x", uint32(c))
}
