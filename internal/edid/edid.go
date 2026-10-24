// Package edid reads a display's identity from its EDID: make, model and
// serial, as outputs are described and matched by.
package edid

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strings"
)

const (
	blockSize       = 128
	descriptorStart = 54
	descriptorSize  = 18
	descriptorCount = 4
	tagSerial       = 0xff
	tagName         = 0xfc
	PNPEnv          = "LYS_WM_PNP_IDS"
)

var defaultPNP = "/usr/share/hwdata/pnp.ids"

func PNPPath(getenv func(string) string) string {
	if p := getenv(PNPEnv); p != "" {
		return p
	}
	return defaultPNP
}

type Vendors map[string]string

func LoadVendors(path string) (Vendors, error) {
	f, err := os.Open(path) //nolint:gosec // the PNP ID database from the build or the environment
	if err != nil {
		return nil, fmt.Errorf("edid: read the PNP ID database (set %s to hwdata's pnp.ids): %w", PNPEnv, err)
	}
	defer f.Close()
	v := Vendors{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		id, name, ok := strings.Cut(sc.Text(), "\t")
		if ok && len(id) == 3 {
			v[id] = strings.TrimSpace(name)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("edid: read %s: %w", path, err)
	}
	return v, nil
}

type Info struct {
	PNP    string
	Make   string
	Model  string
	Serial string
}

var header = []byte{0, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0}

var ErrInvalid = errors.New("edid: invalid")

func Parse(b []byte, vendors Vendors) (Info, error) {
	if len(b) < blockSize || !bytes.Equal(b[:8], header) {
		return Info{}, fmt.Errorf("%w: %d bytes without the EDID header", ErrInvalid, len(b))
	}
	var sum byte
	for _, c := range b[:blockSize] {
		sum += c
	}
	if sum != 0 {
		return Info{}, fmt.Errorf("%w: base block checksum is off by %d", ErrInvalid, sum)
	}
	id := binary.BigEndian.Uint16(b[8:])
	pnp := string([]byte{byte(id>>10&0x1f) + 'A' - 1, byte(id>>5&0x1f) + 'A' - 1, byte(id&0x1f) + 'A' - 1})
	info := Info{PNP: pnp, Make: pnp}
	if name, ok := vendors[pnp]; ok {
		info.Make = name
	}
	for i := range descriptorCount {
		d := b[descriptorStart+i*descriptorSize:][:descriptorSize]
		if d[0] != 0 || d[1] != 0 {
			continue
		}
		text := descriptorText(d[5:])
		switch d[3] {
		case tagName:
			info.Model = text
		case tagSerial:
			info.Serial = text
		}
	}
	if info.Model == "" {
		info.Model = fmt.Sprintf("0x%04X", binary.LittleEndian.Uint16(b[10:]))
	}
	if serial := binary.LittleEndian.Uint32(b[12:]); info.Serial == "" && serial != 0 {
		info.Serial = fmt.Sprintf("0x%08X", serial)
	}
	return info, nil
}

func descriptorText(b []byte) string {
	if i := bytes.IndexByte(b, '\n'); i >= 0 {
		b = b[:i]
	}
	return strings.TrimSpace(string(b))
}

func (i Info) Description() string {
	parts := make([]string, 0, 3)
	for _, p := range []string{i.Make, i.Model, i.Serial} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, " ")
}
