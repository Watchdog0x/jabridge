package main

import (
	"math"
	"strconv"
	"strings"
	"testing"
)

func TestTUITextColorsHaveExplicitReadableContrast(t *testing.T) {
	for name, style := range map[string]string{
		"text": styleText, "title": styleTitle, "selection": styleSelected,
		"actions": styleAction, "warning": styleWarn, "alert": styleAlert,
		"battery": styleBattOK, "low battery": styleBattLow,
		"muted": styleHomeMuted, "card": styleHomeCard, "white": styleHomeWhite,
		"status": styleStatusInfo, "success": styleStatusSuccess, "error": styleStatusError,
	} {
		t.Run(name, func(t *testing.T) {
			parts := strings.Split(style, ";")
			colors := map[string]float64{}
			for i := 0; i+4 < len(parts); i++ {
				if (parts[i] != "38" && parts[i] != "48") || parts[i+1] != "2" {
					continue
				}
				var channels [3]float64
				for j := range channels {
					n, err := strconv.Atoi(parts[i+2+j])
					if err != nil || n < 0 || n > 255 {
						t.Fatal("invalid RGB color", style)
					}
					v := float64(n) / 255
					if v <= 0.04045 {
						channels[j] = v / 12.92
					} else {
						channels[j] = math.Pow((v+0.055)/1.055, 2.4)
					}
				}
				colors[parts[i]] = 0.2126*channels[0] + 0.7152*channels[1] + 0.0722*channels[2]
			}
			if len(colors) != 2 {
				t.Fatal("text depends on the terminal's ANSI palette", style)
			}
			contrast := (max(colors["38"], colors["48"]) + 0.05) / (min(colors["38"], colors["48"]) + 0.05)
			if contrast < 4.5 {
				t.Fatalf("text contrast %.2f is below 4.5:1", contrast)
			}
			t.Logf("contrast %.2f:1", contrast)
		})
	}
}
