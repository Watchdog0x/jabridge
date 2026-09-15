package main

import (
	"fmt"
	"strings"
	"testing"
)

func TestTUIAppUpdateKeysRequireAnExplicitYes(t *testing.T) {
	for _, test := range []struct {
		name   string
		events []keyEvent
		accept bool
	}{
		{"y", []keyEvent{keyRuneBase + 'y'}, true},
		{"Y", []keyEvent{keyRuneBase + 'Y'}, true},
		{"n", []keyEvent{keyRuneBase + 'n'}, false},
		{"N", []keyEvent{keyRuneBase + 'N'}, false},
		{"default", []keyEvent{keyEnter}, false},
		{"escape", []keyEvent{keyEscape}, false},
		{"choose yes", []keyEvent{keyUp, keyEnter}, true},
		{"choose no", []keyEvent{keyUp, keyDown, keyEnter}, false},
		{"pasted yes", []keyEvent{keyPasteBase + 'y', keyEnter}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			yes, decided, accepted := false, false, false
			for i, event := range test.events {
				decided, accepted = handleAppUpdateKey(event, &yes)
				if decided && i != len(test.events)-1 {
					t.Fatal("navigation or paste accepted before confirmation")
				}
			}
			if !decided || accepted != test.accept {
				t.Fatal(decided, accepted)
			}
		})
	}
}

func TestTUIAppUpdateRendersChoicesAndVersionsAtCommonSizes(t *testing.T) {
	for _, size := range [][2]int{{40, 16}, {64, 22}, {80, 24}, {110, 34}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			for _, yes := range []bool{false, true} {
				f := appUpdateFrame(size[0], size[1], "1.0.1", "1.0.2", yes)
				var text strings.Builder
				for row := 1; row <= f.height; row++ {
					text.WriteString(rowText(f, row))
					text.WriteByte('\n')
				}
				for _, want := range []string{"1.0.1", "1.0.2", "Y  Yes", "N  Not now"} {
					if !strings.Contains(text.String(), want) {
						t.Fatalf("missing %q in %v", want, size)
					}
				}
				for row := 1; row <= f.height; row++ {
					line := rowText(f, row)
					if strings.Contains(line, "Y  Yes") || strings.Contains(line, "N  Not now") {
						wantSelected := strings.Contains(line, "Y  Yes") == yes
						selected := false
						for _, c := range f.cells[(row-1)*f.width : row*f.width] {
							selected = selected || c.style == styleHomeSelect
						}
						if selected != wantSelected {
							t.Fatal("choice highlight does not match the selected action")
						}
					}
				}
				if strings.Contains(f.render(), "\033[0;40;97m") {
					t.Fatal("themed frame resets rows to a different background")
				}
			}
		})
	}
}
