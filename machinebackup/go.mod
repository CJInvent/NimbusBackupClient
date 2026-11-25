module machinebackup

go 1.27.0

require (
	clientcommon v0.0.0
	github.com/google/uuid v1.6.0
	github.com/tawesoft/golib/v2 v2.16.0
	golang.org/x/sys v0.50.0
	pbscommon v0.0.0
	snapshot v0.0.0
)

require (
	github.com/alessio/shellescape v1.4.2 // indirect
	github.com/dchest/siphash v1.2.3 // indirect
	github.com/go-ole/go-ole v1.3.0 // indirect
	github.com/klauspost/compress v1.20.5 // indirect
	github.com/rodolfoag/gow32 v0.0.0-20230512144032-1e896a3c51aa // indirect
	github.com/st-matskevich/go-vss v0.4.3 // indirect
	golang.org/x/crypto v0.59.0 // indirect
	golang.org/x/exp v0.0.0-20261124000051-d6646c509b6d // indirect
	golang.org/x/net v0.63.0 // indirect
	golang.org/x/text v0.44.0 // indirect
	golang.org/x/time v0.16.0 // indirect
)

// Local package replacements
replace (
	clientcommon => ../clientcommon
	pbscommon => ../pbscommon
	snapshot => ../snapshot
)
