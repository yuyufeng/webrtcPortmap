//go:build !windows

package main

import (
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/term"
)

// watchResize 监听 SIGWINCH，在本地终端尺寸变化时通知 agent。
func (c *Client) watchResize(fd int) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	go func() {
		for {
			select {
			case <-c.stopChan:
				return
			case <-ch:
				if w, h, err := term.GetSize(fd); err == nil && w > 0 && h > 0 {
					c.sendTermResize(w, h)
				}
			}
		}
	}()
}
