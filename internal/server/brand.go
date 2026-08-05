package server

import (
	"fmt"
	"math"
	"strconv"
)

// rgb is a colour in sRGB, one byte per channel.
type rgb struct{ r, g, b uint8 }

// brandLighten is how far the gradient's far end is mixed towards white. It is
// deliberately gentle: the further the two ends drift apart in luminance, the
// harder it becomes for one text colour to stay legible across both.
const brandLighten = 0.28

// brandCSS builds the stylesheet that carries the header colours. It is served
// as its own asset rather than inlined, because the Content-Security-Policy
// allows stylesheets only from this origin, not inline style blocks.
func brandCSS(hexColor string) []byte {
	base, err := parseHex(hexColor)
	if err != nil {
		// config validates the colour, so this only guards a caller that
		// bypassed it; the default keeps the header readable either way.
		base = rgb{0x1f, 0x5f, 0xa8}
	}
	light := lighten(base, brandLighten)

	return []byte(fmt.Sprintf(":root{--brand:%s;--brand-light:%s;--brand-text:%s}\n",
		base, light, readableOn(base, light)))
}

func (c rgb) String() string {
	return fmt.Sprintf("#%02x%02x%02x", c.r, c.g, c.b)
}

func parseHex(s string) (rgb, error) {
	if len(s) != 7 || s[0] != '#' {
		return rgb{}, fmt.Errorf("colour %q is not #rrggbb", s)
	}
	n, err := strconv.ParseUint(s[1:], 16, 32)
	if err != nil {
		return rgb{}, fmt.Errorf("colour %q: %w", s, err)
	}
	return rgb{uint8(n >> 16), uint8(n >> 8), uint8(n)}, nil
}

// lighten mixes a colour towards white by the given fraction.
func lighten(c rgb, by float64) rgb {
	mix := func(v uint8) uint8 {
		return uint8(math.Round(float64(v) + (255-float64(v))*by))
	}
	return rgb{mix(c.r), mix(c.g), mix(c.b)}
}

// readableOn picks the text colour that stays legible across the whole
// gradient, by scoring each candidate against its worst end.
func readableOn(ends ...rgb) rgb {
	candidates := []rgb{{0xff, 0xff, 0xff}, {0x11, 0x11, 0x11}}

	best, bestScore := candidates[0], -1.0
	for _, candidate := range candidates {
		worst := math.Inf(1)
		for _, end := range ends {
			worst = math.Min(worst, contrastRatio(candidate, end))
		}
		if worst > bestScore {
			best, bestScore = candidate, worst
		}
	}
	return best
}

// contrastRatio is the WCAG ratio between two colours, from 1 to 21.
func contrastRatio(a, b rgb) float64 {
	la, lb := relativeLuminance(a), relativeLuminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// relativeLuminance is the WCAG luminance of a colour, from 0 to 1.
func relativeLuminance(c rgb) float64 {
	channel := func(v uint8) float64 {
		f := float64(v) / 255
		if f <= 0.03928 {
			return f / 12.92
		}
		return math.Pow((f+0.055)/1.055, 2.4)
	}
	return 0.2126*channel(c.r) + 0.7152*channel(c.g) + 0.0722*channel(c.b)
}
