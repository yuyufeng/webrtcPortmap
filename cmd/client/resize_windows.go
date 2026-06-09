//go:build windows

package main

import (
	"time"

	"golang.org/x/term"
)

// watchResize 在 Windows 上没有 SIGWINCH，改用轮询检测本地终端尺寸变化。
func (c *Client) watchResize(fd int) {
	go func() {
		lastW, lastH := -1, -1
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-c.stopChan:
				return
			case <-ticker.C:
				if w, h, err := term.GetSize(fd); err == nil && w > 0 && h > 0 {
					if w != lastW || h != lastH {
						lastW, lastH = w, h
						c.sendTermResize(w, h)
					}
				}
			}
		}
	}()
}
