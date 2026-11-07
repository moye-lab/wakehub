package db

import (
	"os"
	"path/filepath"
	"testing"
)

// TestEnsurePrivateDir 覆盖 v0.2.9 问题 11：新建目录 0700，
// 已存在的 0755 目录也被显式收紧；"." 与空路径不做任何事。
func TestEnsurePrivateDir(t *testing.T) {
	root := t.TempDir()

	fresh := filepath.Join(root, "a", "b")
	if err := EnsurePrivateDir(fresh); err != nil {
		t.Fatalf("新建目录失败：%v", err)
	}
	if got := permOf(t, fresh); got != 0o700 {
		t.Errorf("新建目录应为 0700，got %04o", got)
	}

	old := filepath.Join(root, "old")
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatalf("建旧目录失败：%v", err)
	}
	if err := os.Chmod(old, 0o755); err != nil {
		t.Fatalf("chmod 旧目录失败：%v", err)
	}
	if err := EnsurePrivateDir(old); err != nil {
		t.Fatalf("收紧旧目录失败：%v", err)
	}
	if got := permOf(t, old); got != 0o700 {
		t.Errorf("旧目录应被收紧为 0700，got %04o", got)
	}

	if err := EnsurePrivateDir("."); err != nil {
		t.Errorf("\".\" 应直接返回 nil，got %v", err)
	}
	if err := EnsurePrivateDir(""); err != nil {
		t.Errorf("空路径应直接返回 nil，got %v", err)
	}
}

// TestTightenDBFiles 覆盖 v0.2.9 问题 11：库文件与 -wal/-shm 伴生
// 文件收紧为 0600；伴生文件缺失时不报错；库文件缺失时报错。
func TestTightenDBFiles(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "wakehub.db")
	for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatalf("写 %s 失败：%v", p, err)
		}
	}
	if err := TightenDBFiles(dbPath); err != nil {
		t.Fatalf("收紧失败：%v", err)
	}
	for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		if got := permOf(t, p); got != 0o600 {
			t.Errorf("%s 应为 0600，got %04o", p, got)
		}
	}

	// 伴生文件缺失：只收紧库文件，不报错。
	dbPath2 := filepath.Join(root, "wakehub2.db")
	if err := os.WriteFile(dbPath2, []byte("x"), 0o644); err != nil {
		t.Fatalf("写 %s 失败：%v", dbPath2, err)
	}
	if err := TightenDBFiles(dbPath2); err != nil {
		t.Fatalf("伴生文件缺失时不应报错，got %v", err)
	}
	if got := permOf(t, dbPath2); got != 0o600 {
		t.Errorf("库文件应为 0600，got %04o", got)
	}

	// 库文件缺失：报错。
	if err := TightenDBFiles(filepath.Join(root, "nope.db")); err == nil {
		t.Errorf("库文件缺失时应报错")
	}
	// :memory:：不做任何事。
	if err := TightenDBFiles(":memory:"); err != nil {
		t.Errorf(":memory: 应直接返回 nil，got %v", err)
	}
}

func permOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s 失败：%v", path, err)
	}
	return fi.Mode().Perm()
}
