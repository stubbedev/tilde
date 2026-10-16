package gpu

import (
	"runtime"
	"sync"

	"golang.org/x/sys/unix"
)

var pinned = struct {
	sync.Mutex
	current map[int]*Display
}{current: map[int]*Display{}}

func PinThread() (unpin func() error) {
	runtime.LockOSThread()
	tid := unix.Gettid()
	pinned.Lock()
	_, nested := pinned.current[tid]
	if !nested {
		pinned.current[tid] = nil
	}
	pinned.Unlock()
	return func() error {
		defer runtime.UnlockOSThread()
		if nested {
			return nil
		}
		pinned.Lock()
		d := pinned.current[tid]
		delete(pinned.current, tid)
		pinned.Unlock()
		if d == nil {
			return nil
		}
		return d.Release()
	}
}

func (d *Display) Enter() error {
	runtime.LockOSThread()
	if d.lib.egl.getCurrentContext() == d.ctx {
		return nil
	}
	if err := d.MakeCurrent(); err != nil {
		runtime.UnlockOSThread()
		return err
	}
	return nil
}

func (d *Display) Leave() error {
	defer runtime.UnlockOSThread()
	tid := unix.Gettid()
	pinned.Lock()
	_, keep := pinned.current[tid]
	if keep {
		pinned.current[tid] = d
	}
	pinned.Unlock()
	if keep {
		return nil
	}
	return d.Release()
}

func forget(d *Display) {
	pinned.Lock()
	defer pinned.Unlock()
	for tid, held := range pinned.current {
		if held == d {
			pinned.current[tid] = nil
		}
	}
}
