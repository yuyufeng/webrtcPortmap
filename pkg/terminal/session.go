// Package terminal 提供一个跨平台、持久化的 PTY 终端会话。
//
// 设计要点：
//   - 终端进程（cmd/powershell/sh/bash）通过 PTY 启动，生命周期独立于
//     任何 WebRTC 连接。连接断开时只 Detach（解除输出回调），进程继续运行。
//   - 输出始终被读取并写入一个环形缓冲区（即使当前没有控制端连接），
//     从而避免子进程因管道写满而阻塞。
//   - 控制端重新连接后调用 Attach，可立即拿到环形缓冲区快照（历史回放），
//     之后的实时输出通过 sink 回调推送，做到“断线不重置反馈”。
package terminal

import (
	"errors"
	"os"
	"runtime"
	"strings"
	"sync"

	"github.com/aymanbagabas/go-pty"
)

// ErrClosed 表示会话已关闭或子进程已退出。
var ErrClosed = errors.New("terminal: session closed")

const defaultBufferSize = 256 * 1024 // 256KB 回放缓冲

// Config 描述一个终端会话的启动参数。
type Config struct {
	Shell      string   // 可执行 shell（空则按平台自动选择）
	Args       []string // 额外参数
	BufferSize int      // 环形缓冲字节数（<=0 使用默认 256KB）
	Cols       int      // 初始列数
	Rows       int      // 初始行数
	Env        []string // 环境变量（空则继承当前进程）
	Dir        string   // 工作目录（空则当前目录）
}

// Session 是一个持久的 PTY 终端会话。
type Session struct {
	shell string
	pty   pty.Pty
	cmd   *pty.Cmd

	mu       sync.Mutex
	ring     *ringBuffer
	sink     func([]byte)
	closed   bool
	exited   bool
	exitCode int
	onExit   func(int)
	cols     int
	rows     int
}

// New 创建并启动一个终端会话，读循环与等待循环随即在后台运行。
func New(cfg Config) (*Session, error) {
	shell := ResolveShell(cfg.Shell)
	bufSize := cfg.BufferSize
	if bufSize <= 0 {
		bufSize = defaultBufferSize
	}
	cols := cfg.Cols
	if cols <= 0 {
		cols = 80
	}
	rows := cfg.Rows
	if rows <= 0 {
		rows = 24
	}

	p, err := pty.New()
	if err != nil {
		return nil, err
	}

	c := p.Command(shell, cfg.Args...)
	if len(cfg.Env) > 0 {
		c.Env = cfg.Env
	} else {
		c.Env = defaultEnv()
	}
	if strings.TrimSpace(cfg.Dir) != "" {
		c.Dir = cfg.Dir
	}

	// 初始尺寸（失败不致命）
	_ = p.Resize(cols, rows)

	if err := c.Start(); err != nil {
		_ = p.Close()
		return nil, err
	}

	s := &Session{
		shell: shell,
		pty:   p,
		cmd:   c,
		ring:  newRingBuffer(bufSize),
		cols:  cols,
		rows:  rows,
	}

	go s.readLoop()
	go s.waitLoop()
	return s, nil
}

// readLoop 持续读取 PTY 输出，写入环形缓冲并推送给当前 sink。
func (s *Session) readLoop() {
	buf := make([]byte, 32*1024)
	for {
		n, err := s.pty.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])

			s.mu.Lock()
			s.ring.append(chunk)
			sink := s.sink
			s.mu.Unlock()

			if sink != nil {
				sink(chunk)
			}
		}
		if err != nil {
			return
		}
	}
}

// waitLoop 等待子进程退出并记录退出码。
func (s *Session) waitLoop() {
	err := s.cmd.Wait()
	code := 0
	if s.cmd.ProcessState != nil {
		code = s.cmd.ProcessState.ExitCode()
	} else if err != nil {
		code = -1
	}

	s.mu.Lock()
	s.exited = true
	s.exitCode = code
	onExit := s.onExit
	s.mu.Unlock()

	if onExit != nil {
		onExit(code)
	}
}

// Attach 设置实时输出回调，并返回当前回放缓冲快照（原样字节）。
// 快照与 sink 在同一把锁内切换，保证不漏字节、不重复字节。
func (s *Session) Attach(sink func([]byte)) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sink = sink
	return s.ring.snapshot()
}

// AttachWithReplay 在同一把锁内先回放历史快照、再挂接实时 sink，
// 从而严格保证“回放在前、实时在后”的字节顺序，且不漏不重。
// replay 会在锁内同步调用一次（仅一次快照发送），readLoop 的后续实时
// 推送必须等待该锁释放，因此一定排在回放之后。
func (s *Session) AttachWithReplay(replay func(snapshot []byte), sink func([]byte)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if replay != nil {
		replay(s.ring.snapshot())
	}
	s.sink = sink
}

// Detach 解除实时输出回调（连接断开时调用），进程与缓冲继续保留。
func (s *Session) Detach() {
	s.mu.Lock()
	s.sink = nil
	s.mu.Unlock()
}

// Write 把控制端的键盘输入写入 PTY。
func (s *Session) Write(p []byte) error {
	s.mu.Lock()
	bad := s.closed || s.exited
	s.mu.Unlock()
	if bad {
		return ErrClosed
	}
	_, err := s.pty.Write(p)
	return err
}

// Resize 调整 PTY 窗口大小。
func (s *Session) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return nil
	}
	s.mu.Lock()
	s.cols = cols
	s.rows = rows
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return ErrClosed
	}
	return s.pty.Resize(cols, rows)
}

// SetOnExit 注册子进程退出回调。
func (s *Session) SetOnExit(fn func(code int)) {
	s.mu.Lock()
	s.onExit = fn
	exited := s.exited
	code := s.exitCode
	s.mu.Unlock()
	// 若进程已退出，立即补发一次回调，避免错过通知。
	if exited && fn != nil {
		fn(code)
	}
}

// Alive 返回会话是否仍在运行。
func (s *Session) Alive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closed && !s.exited
}

// Shell 返回实际使用的 shell 路径。
func (s *Session) Shell() string { return s.shell }

// Close 终止子进程并释放 PTY 资源。
func (s *Session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.sink = nil
	cmd := s.cmd
	s.mu.Unlock()

	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	return s.pty.Close()
}

// ResolveShell 把简写（cmd/powershell/bash/sh）解析为可执行路径；
// 空字符串时按平台返回默认 shell。
func ResolveShell(name string) string {
	n := strings.TrimSpace(name)
	if n == "" {
		return DefaultShell()
	}
	if runtime.GOOS == "windows" {
		switch strings.ToLower(n) {
		case "cmd", "cmd.exe":
			if c := os.Getenv("ComSpec"); c != "" {
				return c
			}
			return "cmd.exe"
		case "powershell", "powershell.exe", "ps":
			return "powershell.exe"
		case "pwsh", "pwsh.exe":
			return "pwsh.exe"
		}
		return n
	}
	switch n {
	case "bash":
		return "/bin/bash"
	case "sh":
		return "/bin/sh"
	}
	return n
}

// DefaultShellArgs 为已解析的 shell 返回一组合理的默认启动参数。
// 仅当用户未显式提供 -terminal-args 时使用。
//
// Windows 的 PowerShell（5.1 powershell.exe / 7 pwsh.exe）默认
// ExecutionPolicy 常为 Restricted，会拦掉所有 .ps1（包括用户脚本、
// $PROFILE，以及大量以 .ps1 包装的命令行工具如 npm/yarn/venv 激活脚本），
// 导致“脚本和程序几乎跑不起来”。这里默认补上 -ExecutionPolicy Bypass，
// 让内嵌终端开箱即用；-NoLogo 去掉启动横幅。
// 终端运行在 agent 所属机器、且建立 P2P 后还需本地密码鉴权，放开脚本执行
// 符合该“自有开发机远程终端”的使用场景。
func DefaultShellArgs(resolvedShell string) []string {
	if runtime.GOOS != "windows" {
		return nil
	}
	base := strings.ToLower(resolvedShell)
	if i := strings.LastIndexAny(base, `\/`); i >= 0 {
		base = base[i+1:]
	}
	switch base {
	case "powershell.exe", "pwsh.exe":
		return []string{"-NoLogo", "-ExecutionPolicy", "Bypass"}
	}
	return nil
}

// DefaultShell 按平台选择一个合理的默认 shell。
func DefaultShell() string {
	if runtime.GOOS == "windows" {
		if c := os.Getenv("ComSpec"); c != "" {
			return c
		}
		return "cmd.exe"
	}
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	for _, cand := range []string{"/bin/bash", "/bin/sh"} {
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}
	return "/bin/sh"
}

func defaultEnv() []string {
	env := os.Environ()
	if runtime.GOOS != "windows" {
		hasTerm := false
		for _, e := range env {
			if strings.HasPrefix(e, "TERM=") {
				hasTerm = true
				break
			}
		}
		if !hasTerm {
			env = append(env, "TERM=xterm-256color")
		}
	}
	return env
}
