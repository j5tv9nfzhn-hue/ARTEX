package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/Autumn-27/artex/config"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/server"
	"golang.org/x/term"
)

// resetPasswordEnv carries the new password to the reset mode. It is read from the
// environment rather than a flag so the secret never appears in the process argv
// (visible to every other user via `ps` / the Windows process list).
const resetPasswordEnv = "ARTEX_RESET_NEWPASS"

// runResetPassword implements `artex -reset-password`: open the SQLite store, write
// a new bcrypt hash for the fixed ARTEX admin account, and exit.
//
// It runs as a one-shot ahead of every server boot step — no port to claim, no
// self-update to apply, no background worker — because an operator locked out of a
// compromised instance needs this to work even when the normal startup path would
// not (a poisoned LLM profile, a port clash, or a service that will not come up).
func runResetPassword() int {
	// The master key must be installed before the store is touched, exactly as
	// normal startup does: a settings value written as ciphertext cannot be read
	// back without it, and SetSetting's read-modify-write would fail.
	if err := server.InstallCredentialKey(config.BaseDir()); err != nil {
		fmt.Fprintf(os.Stderr, "凭据主密钥初始化失败：%v\n", err)
		return 1
	}
	pg, err := db.OpenConfigured()
	if err != nil {
		fmt.Fprintf(os.Stderr, "打开数据库失败：%v\n", err)
		return 1
	}
	defer pg.Close()

	fmt.Fprintf(os.Stderr, "数据库：%s\n", config.SQLitePath())

	pw, err := readNewPassword()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	if err := server.ResetAdminPassword(pg, pw); err != nil {
		if errors.Is(err, server.ErrPasswordTooShort) {
			fmt.Fprintln(os.Stderr, "新密码不符合长度要求（至少 8 个字符）。")
		} else {
			fmt.Fprintf(os.Stderr, "写入失败：%v\n", err)
		}
		return 1
	}
	fmt.Println("✓ ARTEX 管理员密码已重置，请以 ARTEX 和新密码登录，无需重启服务。")
	fmt.Println("  既有会话 token 立即失效（签名密钥由密码哈希派生）。若用于账户被抢占后的恢复，请同时检查自定义工具、MCP、LLM 与通知配置。")
	return 0
}

// readNewPassword takes the password from resetPasswordEnv when set (non-interactive
// deployments, and what reset-password.sh uses), and otherwise prompts twice.
func readNewPassword() (string, error) {
	if v := os.Getenv(resetPasswordEnv); v != "" {
		return v, nil
	}
	first, err := promptSecret("输入新密码（登录用户名固定为 ARTEX）：")
	if err != nil {
		return "", err
	}
	second, err := promptSecret("再次输入以确认：")
	if err != nil {
		return "", err
	}
	if first != second {
		return "", errors.New("两次输入不一致")
	}
	if first == "" {
		return "", errors.New("密码不能为空")
	}
	return first, nil
}

// promptSecret reads one line without echoing. With stdin redirected (a pipe or a
// here-string) there is no terminal to silence, so it falls back to a plain line
// read — callers piping a secret in are expected to use resetPasswordEnv instead.
func promptSecret(prompt string) (string, error) {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		fmt.Fprint(os.Stderr, prompt)
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
