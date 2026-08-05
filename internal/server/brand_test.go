package server

import (
	"math"
	"strings"
	"testing"
)

func TestBrandCSS(t *testing.T) {
	got := string(brandCSS("#1f5fa8"))

	for _, want := range []string{":root{", "--brand:#1f5fa8;", "--brand-light:#", "--brand-text:#"} {
		if !strings.Contains(got, want) {
			t.Errorf("brandCSS() = %q, want it to contain %q", got, want)
		}
	}
	// A colour config never validated must still produce a usable header.
	if fallback := string(brandCSS("not a colour")); !strings.Contains(fallback, "--brand:#1f5fa8;") {
		t.Errorf("brandCSS(invalid) = %q, want the default colour", fallback)
	}
}

// The second stop is the same hue, lighter — that is what makes it a gradient
// rather than two unrelated colours.
func TestLighten(t *testing.T) {
	tests := []struct {
		name string
		in   rgb
	}{
		{"a mid blue", rgb{0x1f, 0x5f, 0xa8}},
		{"black", rgb{0x00, 0x00, 0x00}},
		{"a dark red", rgb{0x8b, 0x00, 0x00}},
		{"already white", rgb{0xff, 0xff, 0xff}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := lighten(tt.in, brandLighten)

			if out.r < tt.in.r || out.g < tt.in.g || out.b < tt.in.b {
				t.Errorf("lighten(%s) = %s, which is darker in some channel", tt.in, out)
			}
			if relativeLuminance(out) < relativeLuminance(tt.in) {
				t.Errorf("lighten(%s) = %s, which is not lighter", tt.in, out)
			}
			if tt.in != (rgb{0xff, 0xff, 0xff}) && out == tt.in {
				t.Errorf("lighten(%s) did not change the colour", tt.in)
			}
		})
	}
}

// The brand text has to stay legible over both ends of the gradient, whatever
// colour is configured.
func TestReadableOnEveryBrandColour(t *testing.T) {
	colours := []string{
		"#1f5fa8", // the default blue
		"#000000", "#ffffff",
		"#ff0000", "#00ff00", "#0000ff",
		"#ffff00", // yellow: white text would fail here
		"#7a7a7a", // mid grey: the worst case for either choice
		"#c8102e", "#0b3d2e", "#f5f5dc",
	}

	for _, hex := range colours {
		base, err := parseHex(hex)
		if err != nil {
			t.Fatalf("parseHex(%q): %v", hex, err)
		}
		light := lighten(base, brandLighten)
		text := readableOn(base, light)

		worst := math.Min(contrastRatio(text, base), contrastRatio(text, light))
		// 3:1 is the WCAG floor for large text, which the brand is: 1.45rem
		// at weight 800.
		if worst < 3 {
			t.Errorf("brand %s: text %s has contrast %.2f against its worst end, want >= 3",
				hex, text, worst)
		}
		// And the alternative must not have been the better pick.
		other := rgb{0xff, 0xff, 0xff}
		if text == other {
			other = rgb{0x11, 0x11, 0x11}
		}
		otherWorst := math.Min(contrastRatio(other, base), contrastRatio(other, light))
		if otherWorst > worst {
			t.Errorf("brand %s: chose %s (%.2f) over %s (%.2f)", hex, text, worst, other, otherWorst)
		}
	}
}

func TestContrastRatio(t *testing.T) {
	white, black := rgb{0xff, 0xff, 0xff}, rgb{0x00, 0x00, 0x00}

	if got := contrastRatio(white, black); math.Abs(got-21) > 0.01 {
		t.Errorf("contrastRatio(white, black) = %.2f, want 21", got)
	}
	if got := contrastRatio(white, white); math.Abs(got-1) > 0.01 {
		t.Errorf("contrastRatio(white, white) = %.2f, want 1", got)
	}
	// The ratio does not depend on argument order.
	if a, b := contrastRatio(white, black), contrastRatio(black, white); a != b {
		t.Errorf("contrastRatio is not symmetric: %.2f vs %.2f", a, b)
	}
}

func TestParseHex(t *testing.T) {
	if c, err := parseHex("#1f5fa8"); err != nil || c != (rgb{0x1f, 0x5f, 0xa8}) {
		t.Errorf("parseHex(#1f5fa8) = %v, %v", c, err)
	}
	for _, bad := range []string{"", "1f5fa8", "#12345", "#gggggg", "#1f5fa8ff"} {
		if _, err := parseHex(bad); err == nil {
			t.Errorf("parseHex(%q) succeeded, want an error", bad)
		}
	}
}
