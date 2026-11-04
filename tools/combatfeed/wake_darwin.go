package main

import (
	"os"
	"syscall"
	"time"
)

// waker blocks until the followed file is written to, or the timeout passes.
// On macOS it asks the kernel (kqueue, EVFILT_VNODE) to wake it the moment
// tcpdump appends a packet, so a packet reaches the feed in microseconds
// instead of waiting out a poll interval.
type waker struct {
	kq int
	f  *os.File
}

func newWaker(path string) *waker {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	kq, err := syscall.Kqueue()
	if err != nil {
		f.Close()
		return nil
	}
	ev := syscall.Kevent_t{
		Ident:  uint64(f.Fd()),
		Filter: syscall.EVFILT_VNODE,
		Flags:  syscall.EV_ADD | syscall.EV_CLEAR,
		Fflags: syscall.NOTE_WRITE | syscall.NOTE_EXTEND,
	}
	if _, err := syscall.Kevent(kq, []syscall.Kevent_t{ev}, nil, nil); err != nil {
		syscall.Close(kq)
		f.Close()
		return nil
	}
	return &waker{kq: kq, f: f}
}

// wait returns when the file grows or after timeout; with no waker it sleeps.
func (w *waker) wait(timeout time.Duration) {
	if w == nil {
		time.Sleep(timeout)
		return
	}
	ts := syscall.NsecToTimespec(timeout.Nanoseconds())
	out := make([]syscall.Kevent_t, 1)
	_, _ = syscall.Kevent(w.kq, nil, out, &ts)
}
