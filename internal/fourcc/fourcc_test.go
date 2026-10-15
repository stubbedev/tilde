package fourcc

import "testing"

func TestFormatsPrintAsTheirCode(t *testing.T) {
	for _, tc := range []struct {
		f    Format
		want string
	}{{XRGB8888, "XR24"}, {ARGB8888, "AR24"}, {NV12, "NV12"}, {YUV420, "YU12"}, {Format(0x01020304), "0x01020304"}} {
		if got := tc.f.String(); got != tc.want {
			t.Errorf("%#x prints as %q, want %q", uint32(tc.f), got, tc.want)
		}
	}
}

func TestModifiersPrintByName(t *testing.T) {
	for _, tc := range []struct {
		m    Modifier
		want string
	}{{Linear, "linear"}, {Invalid, "invalid"}, {Modifier(0x0100000000000001), "0x0100000000000001"}} {
		if got := tc.m.String(); got != tc.want {
			t.Errorf("%#x prints as %q, want %q", uint64(tc.m), got, tc.want)
		}
	}
}
