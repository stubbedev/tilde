package edid

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const odysseyG5 = "00ffffffffffff004c2d807038574a30182001" +
	"04b54628783b43a5ae5244b0260f5054bfef80714f810081c081809500a9c0b3000101" +
	"98e200a0a0a0295008403500ba892100001a000000fd003090d6d63b010a202020202020" +
	"000000fc004c43333247357854" + "0a20202020000000ff00484e415436303033373" + "00a202001c4"

func base(t *testing.T) []byte {
	t.Helper()
	b, err := hex.DecodeString(odysseyG5)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func vendors(t *testing.T) Vendors {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pnp.ids")
	if err := os.WriteFile(path, []byte("GSM\tLG Electronics\nSAM\tSamsung Electric Company\nbad line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	v, err := LoadVendors(path)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestParseARealMonitor(t *testing.T) {
	info, err := Parse(base(t), vendors(t))
	if err != nil {
		t.Fatal(err)
	}
	if want := (Info{PNP: "SAM", Make: "Samsung Electric Company", Model: "LC32G5xT", Serial: "HNAT600370"}); info != want {
		t.Fatalf("%+v, want %+v", info, want)
	}
	if d := info.Description(); d != "Samsung Electric Company LC32G5xT HNAT600370" {
		t.Fatalf("description %q", d)
	}
}

func TestFallbacksAndRejection(t *testing.T) {
	b := base(t)
	info, err := Parse(b, nil)
	if err != nil || info.Make != "SAM" {
		t.Fatalf("%+v, %v: an unknown vendor keeps its PNP ID", info, err)
	}
	stripped := append([]byte(nil), b[:128]...)
	for i := 54; i < 126; i++ {
		stripped[i] = 1
	}
	var sum byte
	for _, c := range stripped[:127] {
		sum += c
	}
	stripped[127] = -sum
	info, err = Parse(stripped, vendors(t))
	if err != nil {
		t.Fatal(err)
	}
	if info.Model != "0x7080" || info.Serial != "0x304A5738" || info.Description() != "Samsung Electric Company 0x7080 0x304A5738" {
		t.Fatalf("without descriptors: %+v", info)
	}
	bad := append([]byte(nil), b...)
	bad[20]++
	if _, err := Parse(bad, nil); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("a bad checksum: %v", err)
	}
	if _, err := Parse(b[:64], nil); err == nil {
		t.Fatal("a short EDID was accepted")
	}
	if _, err := LoadVendors("/nonexistent/pnp.ids"); err == nil || !strings.Contains(err.Error(), PNPEnv) {
		t.Fatalf("a missing database: %v", err)
	}
}
