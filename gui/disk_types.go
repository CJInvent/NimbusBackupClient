package main

import "github.com/tizbac/proxmoxbackupclient_go/gui/api"

// PhysicalDiskInfo is the wire shape in gui/api (machine.go): what the disk
// enumeration returns is what GET /disks serves, with no twin to convert from.
// It used to be declared twice, once in each platform's machine-backup file.
type PhysicalDiskInfo = api.PhysicalDisk
