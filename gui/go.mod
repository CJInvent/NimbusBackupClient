module github.com/tizbac/proxmoxbackupclient_go/gui

go 1.26.0

require (
	controlplane v0.0.0
	github.com/kardianos/service v1.3.0
	github.com/tizbac/proxmoxbackupclient_go/gui/api v0.0.0
	golang.org/x/crypto v0.58.0
	golang.org/x/sys v0.49.0
	imagebrowse v0.0.0
	pbscommon v0.0.0
	retry v0.0.0
	security v0.0.0
	snapshot v0.0.0
)

require (
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/dchest/siphash v1.2.3 // indirect
	github.com/go-ole/go-ole v1.3.0 // indirect
	github.com/klauspost/compress v1.17.9 // indirect
	github.com/mattn/go-isatty v0.0.19 // indirect
	github.com/st-matskevich/go-vss v0.3.3 // indirect
	github.com/stretchr/testify v1.8.4 // indirect
	golang.org/x/net v0.61.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	golang.org/x/time v0.5.0 // indirect
	www.velocidex.com/golang/go-ntfs v0.2.0 // indirect
)

// Local package replacements to use sibling directories
replace (
	clientcommon => ../clientcommon
	controlplane => ../controlplane
	github.com/tizbac/proxmoxbackupclient_go/gui/api => ./api
	imagebrowse => ../imagebrowse
	pbscommon => ../pbscommon
	retry => ../pkg/retry
	security => ../pkg/security
	snapshot => ../snapshot
)
