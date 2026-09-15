package main

import "strings"

// Explicit RGB colors keep text and selections readable independently of the
// terminal's configurable ANSI palette. All TUI screens share these colors.
const (
	styleHomeBase   = "0;38;2;215;226;237;48;2;14;22;32"
	styleHomeMuted  = "0;38;2;139;160;178;48;2;14;22;32"
	styleHomeBorder = "0;38;2;54;81;98;48;2;14;22;32"
	styleHomeTitle  = "1;38;2;100;229;192;48;2;14;22;32"
	styleHomeWhite  = "1;38;2;238;245;250;48;2;14;22;32"
	styleHomeCard   = "0;38;2;185;205;221;48;2;23;37;51"
	styleHomeSelect = "1;38;2;10;31;31;48;2;100;229;192"
	styleHomeWarn   = "1;38;2;242;195;108;48;2;14;22;32"
)

func drawThemedBox(f *frame, top, left, right, bottom int, label string) {
	if right-left < 4 || bottom-top < 2 {
		return
	}
	line := strings.Repeat("─", right-left-1)
	f.setText(top, left, "╭"+line+"╮", styleHomeBorder)
	f.setText(bottom, left, "╰"+line+"╯", styleHomeBorder)
	for row := top + 1; row < bottom; row++ {
		f.setText(row, left, "│", styleHomeBorder)
		f.setText(row, right, "│", styleHomeBorder)
	}
	f.setText(top, left+3, trimToWidth(" "+label+" ", right-left-5), styleHomeTitle)
}

func drawingHomeBox() {
	restore := screen.withoutClip()
	defer restore()
	left, right, bottom := panelBounds()
	drawThemedBox(screen, 4, left, right, bottom, "DEVICE CONTROL")
}
