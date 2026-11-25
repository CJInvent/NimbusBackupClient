module pbsnbd

go 1.27.0

require (
	github.com/gdamore/tcell/v2 v2.13.10
	github.com/pojntfx/go-nbd v0.3.2
	github.com/rivo/tview v0.42.0
	pbscommon v0.0.0
)

require (
	github.com/dchest/siphash v1.2.3 // indirect
	github.com/gdamore/encoding v1.0.1 // indirect
	github.com/klauspost/compress v1.20.5 // indirect
	github.com/lucasb-eyer/go-colorful v1.4.1 // indirect
	github.com/pilebones/go-udev v0.10.0 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	golang.org/x/crypto v0.59.0 // indirect
	golang.org/x/net v0.63.0 // indirect
	golang.org/x/sys v0.50.0 // indirect
	golang.org/x/term v0.48.0 // indirect
	golang.org/x/text v0.44.0 // indirect
	golang.org/x/time v0.16.0 // indirect
)

// Local package replacements
replace pbscommon => ../pbscommon
