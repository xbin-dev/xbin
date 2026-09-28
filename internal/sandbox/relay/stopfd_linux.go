//go:build linux

package relay

import (
	"reflect"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

// stopFDs digs out of a gVisor fdbased endpoint the eventfds its inbound
// dispatchers wake on: gVisor makes one per TUN fd and never closes it (the
// endpoint's Close is empty), so every relay used to leak one — a backend's,
// a terminal's, a confined tool run's, each tile sandbox start. They are
// read by reflection (reading an unexported field is allowed; nothing is
// written), so this leans on gVisor's internals: a bump that moves them
// finds none here, leaks as before, and fails TestCloseReleasesFDs.
func stopFDs(ep stack.LinkEndpoint) []int {
	v := reflect.ValueOf(ep)
	if v.Kind() != reflect.Pointer || v.IsNil() || v.Elem().Kind() != reflect.Struct {
		return nil
	}
	ds := v.Elem().FieldByName("inboundDispatchers")
	if !ds.IsValid() || ds.Kind() != reflect.Slice {
		return nil
	}
	var out []int
	for i := 0; i < ds.Len(); i++ {
		d := ds.Index(i)
		for (d.Kind() == reflect.Interface || d.Kind() == reflect.Pointer) && !d.IsNil() {
			d = d.Elem()
		}
		if d.Kind() != reflect.Struct {
			continue
		}
		if efd := d.FieldByName("EFD"); efd.IsValid() && efd.CanInt() && efd.Int() >= 0 { // stopfd.StopFD's
			out = append(out, int(efd.Int()))
		}
	}
	return out
}

// closeFDs closes fds (the dispatchers' stop fds, once they have stopped).
func closeFDs(fds []int) {
	for _, fd := range fds {
		unix.Close(fd)
	}
}
