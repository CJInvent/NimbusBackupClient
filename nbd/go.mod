module pbsnbd

go 1.26

require (
	github.com/gdamore/tcell/v2 v2.8.1
	github.com/pojntfx/go-nbd v0.3.2
	github.com/rivo/tview v0.42.0
	pbscommon v0.0.0
)

require (
	github.com/dchest/siphash v1.2.3 // indirect
	github.com/gdamore/encoding v1.0.1 // indirect
	github.com/klauspost/compress v1.17.9 // indirect
	github.com/lucasb-eyer/go-colorful v1.2.0 // indirect
	github.com/mattn/go-runewidth v0.0.16 // indirect
	github.com/pilebones/go-udev v0.9.0 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	golang.org/x/crypto v0.51.0 // indirect
	golang.org/x/net v0.55.0 // indirect
	golang.org/x/sys v0.45.0 // indirect
	golang.org/x/term v0.43.0 // indirect
	golang.org/x/text v0.39.0 // indirect
	golang.org/x/time v0.5.0 // indirect
)

// Local package replacements
replace pbscommon => ../pbscommon
