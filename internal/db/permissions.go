package db

import (
	"errors"
	"os"
)

// --- 文件权限（v0.2.9 问题 11）---
//
// 数据目录 0700、数据库文件 0600、备份目录 0700、备份文件 0600。
// MkdirAll 只对新建目录生效，已存在的 0755 目录不会被改掉，
// 所以建目录后必须再显式 Chmod（升级时收紧旧目录就靠它）。

// EnsurePrivateDir 建目录（不存在时）并把权限收紧为 0700。
// dir 为空或 "."（如 :memory: 库的父目录）时什么都不做。
func EnsurePrivateDir(dir string) error {
	if dir == "" || dir == "." {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(dir, 0o700)
}

// TightenDBFiles 把数据库文件及 WAL 伴生文件（-wal/-shm）的权限
// 收紧为 0600；伴生文件不存在时跳过（不报错）。
// 注意：SQLite 建 -wal/-shm 时会沿用数据库文件的权限，所以启动时
// 先收紧一次，之后重建的伴生文件自然也是 0600。
func TightenDBFiles(dbPath string) error {
	if dbPath == "" || dbPath == ":memory:" {
		return nil
	}
	if err := os.Chmod(dbPath, 0o600); err != nil {
		return err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Chmod(dbPath+suffix, 0o600); err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}
