//go:build !darwin

package main

import "time"

// waker falls back to sleeping where kqueue file events aren't available.
type waker struct{}

func newWaker(string) *waker { return nil }

func (w *waker) wait(timeout time.Duration) { time.Sleep(timeout) }
