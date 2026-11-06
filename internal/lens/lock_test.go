package lens

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLockProcessHelper(t *testing.T) {
	root := os.Getenv("RISKLENS_TEST_LOCK_ROOT")
	if root == "" {
		return
	}
	e, err := New(Config{Root: root}, &fakeEmbed{})
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println("ready")
	if os.Getenv("RISKLENS_TEST_LOCK_EXIT") == "abrupt" {
		os.Exit(0)
	}
	io.Copy(io.Discard, os.Stdin)
	e.Close()
}
func TestOSLockLifecycleAndLegacyMigration(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module locktest\ngo 1.26.0\n"), 0600)
	cmd := exec.Command(os.Args[0], "-test.run=^TestLockProcessHelper$")
	cmd.Env = append(os.Environ(), "RISKLENS_TEST_LOCK_ROOT="+root)
	out, _ := cmd.StdoutPipe()
	in, _ := cmd.StdinPipe()
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		in.Close()
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	}()
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "ready" {
		t.Fatal(line, err)
	}
	if e, err := New(Config{Root: root}, &fakeEmbed{}); err == nil {
		e.Close()
		t.Fatal("allowed concurrent cache owner")
	}
	in.Close()
	if err = cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	e, err := New(Config{Root: root}, &fakeEmbed{})
	if err != nil {
		t.Fatal("EOF did not release lock", err)
	}
	e.Close()
	e.Close()
	// Simulate process exit without Engine.Close: OS must recover ownership.
	crash := exec.Command(os.Args[0], "-test.run=^TestLockProcessHelper$")
	crash.Env = append(os.Environ(), "RISKLENS_TEST_LOCK_ROOT="+root, "RISKLENS_TEST_LOCK_EXIT=abrupt")
	if b, err := crash.CombinedOutput(); err != nil {
		t.Fatal(string(b), err)
	}
	e, err = New(Config{Root: root}, &fakeEmbed{})
	if err != nil {
		t.Fatal("abrupt exit left stale lock", err)
	}
	e.Close()
	lock := filepath.Join(root, ".risklens", "server.lock")
	os.WriteFile(lock, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0600)
	if e, err := New(Config{Root: root}, &fakeEmbed{}); err == nil {
		e.Close()
		t.Fatal("overlapped live legacy PID")
	}
	os.WriteFile(lock, []byte(fmt.Sprintf("%d\n", crash.Process.Pid)), 0600)
	e, err = New(Config{Root: root}, &fakeEmbed{})
	if err != nil {
		t.Fatal("dead legacy marker not migrated", err)
	}
	e.Close()
}

func TestClearCacheSharesOwnershipProtocol(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module cache\ngo 1.26.0\n"), 0600)
	c := Config{Root: root}
	e, err := New(c, &fakeEmbed{})
	if err != nil {
		t.Fatal(err)
	}
	cache := e.C.Cache
	for _, name := range []string{"vectors.json", "vectors-owned.tmp", "keep.txt"} {
		os.WriteFile(filepath.Join(cache, name), []byte("x"), 0600)
	}
	if _, err = ClearCache(c); err == nil {
		e.Close()
		t.Fatal("cleared active cache")
	}
	e.Close()
	removed, err := ClearCache(c)
	if err != nil || len(removed) != 2 {
		t.Fatal(removed, err)
	}
	if _, err = os.Stat(filepath.Join(cache, "keep.txt")); err != nil {
		t.Fatal("removed unrelated file")
	}
	if _, err = os.Stat(filepath.Join(cache, "server.lock")); err != nil {
		t.Fatal("removed stable lock inode")
	}
	again, err := New(c, &fakeEmbed{})
	if err != nil {
		t.Fatal(err)
	}
	again.Close()
}
