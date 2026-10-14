package drm_test

import (
	"encoding/binary"
	"errors"
	"image"
	"slices"
	"testing"

	"github.com/stubbedev/tilde/internal/drm"
)

func card(t *testing.T) *drm.Device {
	t.Helper()
	d, err := drm.Open("/dev/dri/card0")
	if err != nil {
		t.Skipf("no DRM card here: %v", err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := errors.Join(d.SetClientCap(drm.ClientCapUniversal, 1), d.SetClientCap(drm.ClientCapAtomic, 1)); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestModeResourcesOnThisCard(t *testing.T) {
	d := card(t)
	res, err := d.Resources()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.CRTCs) == 0 || len(res.Connectors) == 0 || res.MaxSize[0] == 0 {
		t.Fatalf("resources %+v", res)
	}
	for _, id := range res.Connectors {
		c, err := d.Connector(id)
		if err != nil {
			t.Fatal(err)
		}
		props, err := d.Properties(id, drm.ObjectConnector)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := props["CRTC_ID"]; !ok {
			t.Errorf("connector %s has no CRTC_ID property: %v", c.Name(), props)
		}
		t.Logf("connector %s: connection %d, %d modes, %dx%d mm", c.Name(), c.Connection, len(c.Modes), c.PhysicalMM[0], c.PhysicalMM[1])
		if c.Connection != drm.Connected {
			continue
		}
		if len(c.Modes) == 0 {
			t.Errorf("connected %s has no modes", c.Name())
		}
		preferred := slices.IndexFunc(c.Modes, drm.Mode.Preferred)
		if preferred < 0 {
			t.Errorf("%s has no preferred mode", c.Name())
			continue
		}
		m := c.Modes[preferred]
		if m.HDisplay == 0 || m.RefreshMHz() < 20000 || m.RefreshMHz() > 1000000 || len(m.Bytes()) != 68 {
			t.Errorf("preferred mode %+v at %d mHz", m, m.RefreshMHz())
		}
		t.Logf("  preferred %dx%d@%d mHz", m.HDisplay, m.VDisplay, m.RefreshMHz())
		if edid, ok := props["EDID"]; ok && edid.Value != 0 {
			blob, err := d.Blob(uint32(edid.Value))
			if err != nil || len(blob) < 128 || blob[0] != 0 || blob[1] != 0xff {
				t.Errorf("EDID blob of %d bytes: %v", len(blob), err)
			}
		}
	}
	for _, id := range res.CRTCs {
		props, err := d.Properties(id, drm.ObjectCRTC)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"ACTIVE", "MODE_ID"} {
			if _, ok := props[name]; !ok {
				t.Errorf("CRTC %d lacks %s", id, name)
			}
		}
	}
}

func TestPlanesAdvertiseFormatsAndModifiers(t *testing.T) {
	d := card(t)
	planes, err := d.Planes()
	if err != nil {
		t.Fatal(err)
	}
	primaries := 0
	for _, id := range planes {
		p, err := d.Plane(id)
		if err != nil {
			t.Fatal(err)
		}
		props, err := d.Properties(id, drm.ObjectPlane)
		if err != nil {
			t.Fatal(err)
		}
		info, err := d.PropertyInfo(props["type"].ID)
		if err != nil {
			t.Fatal(err)
		}
		if props["type"].Value != info.Enums["Primary"] {
			continue
		}
		primaries++
		const xrgb8888 = 'X' | 'R'<<8 | '2'<<16 | '4'<<24
		if !slices.Contains(p.Formats, xrgb8888) {
			t.Errorf("primary plane %d lacks XRGB8888: %v", id, p.Formats)
		}
		in, ok := props["IN_FORMATS"]
		if !ok {
			continue
		}
		blob, err := d.Blob(uint32(in.Value))
		if err != nil {
			t.Fatal(err)
		}
		mods, err := drm.Modifiers(blob, xrgb8888)
		if err != nil || len(mods) == 0 {
			t.Fatalf("XRGB8888 modifiers %v: %v", mods, err)
		}
		t.Logf("primary plane %d: %d formats, XRGB8888 with %d modifiers, IN_FORMATS v%d", id, len(p.Formats), len(mods), binary.NativeEndian.Uint32(blob))
	}
	if primaries == 0 {
		t.Fatal("no primary planes")
	}
}

func TestAtomicRequestsGroupByObject(t *testing.T) {
	var r drm.Request
	r.Add(7, 1, 10)
	r.Add(3, 2, 20)
	r.Add(7, 4, 40)
	r.Add(3, 5, 50)
	objects, counts, props, values := drm.Arrays(&r)
	if !slices.Equal(objects, []uint32{3, 7}) || !slices.Equal(counts, []uint32{2, 2}) || !slices.Equal(props, []uint32{2, 5, 1, 4}) || !slices.Equal(values, []uint64{20, 50, 10, 40}) {
		t.Fatalf("objects %v counts %v props %v values %v", objects, counts, props, values)
	}
	r.Reset()
	allocs := testing.AllocsPerRun(50, func() {
		r.Reset()
		r.Add(7, 1, 10)
		r.Add(3, 2, 20)
		drm.Arrays(&r)
	})
	if allocs != 0 {
		t.Fatalf("%v allocations per reused request", allocs)
	}
}

func TestDumbBuffersScanOutLinearARGB(t *testing.T) {
	d := card(t)
	b, err := d.CreateDumb(image.Pt(64, 32))
	if err != nil {
		t.Fatal(err)
	}
	if b.Pitch < 64*4 || len(b.Pix) < int(b.Pitch)*32 {
		t.Fatalf("pitch %d for %d mapped bytes", b.Pitch, len(b.Pix))
	}
	b.Pix[len(b.Pix)-1] = 0xff
	id, err := d.AddFB2(drm.FB{Width: 64, Height: 32, Format: 'A' | 'R'<<8 | '2'<<16 | '4'<<24, Handles: [4]uint32{b.Handle}, Pitches: [4]uint32{b.Pitch}})
	if err != nil {
		t.Fatal(errors.Join(err, d.DestroyDumb(b)))
	}
	if err := errors.Join(d.RemoveFB(id), d.DestroyDumb(b)); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateDumb(image.Point{}); err == nil {
		t.Fatal("an empty dumb buffer was made")
	}
}
