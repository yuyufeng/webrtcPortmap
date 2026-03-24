// protocol/command.go - 命令解析
package protocol

import (
	"errors"
	"fmt"
	"strings"
)

// ParseCommandString 解析命令行字符串
// 格式: action [-lp local] [-rp remote] [-p protocol]
// 示例: add -lp 0.0.0.0:8080 -rp 127.0.0.1:80 -p tcp
//       remove -id xxx
//       list
func ParseCommandString(input string) (*Command, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, errors.New("empty command")
	}

	parts := strings.Fields(input)
	if len(parts) == 0 {
		return nil, errors.New("empty command")
	}

	action := strings.ToLower(parts[0])
	if action != "add" && action != "remove" && action != "list" {
		return nil, fmt.Errorf("unknown action: %s", action)
	}

	cmd := &Command{
		Action:   action,
		Protocol: "tcp", // 默认TCP
	}

	// 解析参数
	for i := 1; i < len(parts); i++ {
		switch parts[i] {
		case "-lp", "--local":
			if i+1 >= len(parts) {
				return nil, errors.New("missing value for -lp")
			}
			cmd.Local = parts[i+1]
			i++
		case "-rp", "--remote":
			if i+1 >= len(parts) {
				return nil, errors.New("missing value for -rp")
			}
			cmd.Remote = parts[i+1]
			i++
		case "-p", "--protocol":
			if i+1 >= len(parts) {
				return nil, errors.New("missing value for -p")
			}
			protocol := strings.ToLower(parts[i+1])
			if protocol != "tcp" && protocol != "udp" {
				return nil, fmt.Errorf("unsupported protocol: %s", protocol)
			}
			cmd.Protocol = protocol
			i++
		case "-id", "--id":
			if i+1 >= len(parts) {
				return nil, errors.New("missing value for -id")
			}
			cmd.ID = parts[i+1]
			i++
		default:
			return nil, fmt.Errorf("unknown option: %s", parts[i])
		}
	}

	// 验证参数
	if err := validateCommand(cmd); err != nil {
		return nil, err
	}

	return cmd, nil
}

func validateCommand(cmd *Command) error {
	switch cmd.Action {
	case "add":
		if cmd.Local == "" {
			return errors.New("add command requires -lp (local port)")
		}
		if cmd.Remote == "" {
			return errors.New("add command requires -rp (remote port)")
		}
	case "remove":
		if cmd.ID == "" {
			return errors.New("remove command requires -id (mapping id)")
		}
	case "list":
		// list不需要参数
	}
	return nil
}

// String 返回命令的字符串表示
func (c *Command) String() string {
	switch c.Action {
	case "add":
		return fmt.Sprintf("add -lp %s -rp %s -p %s", c.Local, c.Remote, c.Protocol)
	case "remove":
		return fmt.Sprintf("remove -id %s", c.ID)
	case "list":
		return "list"
	default:
		return fmt.Sprintf("unknown: %s", c.Action)
	}
}
