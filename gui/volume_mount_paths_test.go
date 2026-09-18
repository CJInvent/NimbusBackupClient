//go:build windows

package main

import (
	"reflect"
	"testing"
)

func TestVolumeMountPaths(t *testing.T) {
	for _, c := range []struct {
		input []uint16
		want  []string
	}{
		{[]uint16{'C', ':', 92, 0, 'D', ':', 92, 0, 0}, []string{"C:\\", "D:\\"}},
		{[]uint16{0, 'C', ':', 92, 0}, []string{}},
		{[]uint16{'C', ':', 92}, []string{}},
		{nil, []string{}},
	} {
		if got := volumeMountPaths(c.input); !reflect.DeepEqual(got, c.want) {
			t.Fatalf("got %q want %q", got, c.want)
		}
	}
}
