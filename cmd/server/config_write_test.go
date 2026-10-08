package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/livecfg"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
	"github.com/linguo2625469/workbuddy2api-panel/internal/scheduler"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

func testConfigOps() configWriteOps {
	return configWriteOps{os.CreateTemp, os.Rename, func(p string, flags int, mode os.FileMode) (configOutput, error) { return os.OpenFile(p, flags, mode) }}
}

func TestConfigWriteTemporaryFailurePreservesOriginal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	ops := testConfigOps()
	ops.createTemp = func(dir, pattern string) (*os.File, error) {
		f, err := os.CreateTemp(dir, pattern)
		if err != nil {
			return nil, err
		}
		name := f.Name()
		if err := f.Close(); err != nil {
			return nil, err
		}
		// A real read-only descriptor deterministically rejects the temporary write,
		// even when these tests run as root.
		return os.Open(name)
	}
	ops.rename = func(string, string) error { t.Fatal("must not rename incomplete temporary content"); return nil }
	if err := writeConfigFileWithOps(path, []byte("new"), ops); err == nil {
		t.Fatal("expected temporary write failure")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "original" {
		t.Fatalf("original changed: %s, %v", got, err)
	}
	files, _ := os.ReadDir(dir)
	if len(files) != 1 {
		t.Fatalf("temporary files leaked: %v", files)
	}
}

func TestConfigWriteUniqueTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"old":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	// An already-open reader observes the original inode after atomic replacement.
	old, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	sentinel := path + ".tmp"
	if err := os.WriteFile(sentinel, []byte("unrelated"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeConfigFile(path, []byte(`{"new":true}`)); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(sentinel)
	if string(got) != "unrelated" {
		t.Fatalf("fixed temporary file overwritten: %s", got)
	}
	got, _ = os.ReadFile(path)
	if string(got) != `{"new":true}` {
		t.Fatalf("config = %s", got)
	}
	got, _ = io.ReadAll(old)
	if string(got) != `{"old":true}` {
		t.Fatalf("old inode modified: %s", got)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	files, _ := os.ReadDir(dir)
	if len(files) != 2 {
		t.Fatalf("temporary files leaked: %v", files)
	}
}

func TestConfigWriteFailuresPreserveOriginal(t *testing.T) {
	for _, stage := range []string{"create permission", "rename permission", "rename other", "fallback open"} {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.json")
			if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
				t.Fatal(err)
			}
			ops := testConfigOps()
			cause := error(os.ErrPermission)
			switch stage {
			case "create permission":
				ops.createTemp = func(string, string) (*os.File, error) { return nil, &os.PathError{Op: "open", Path: path, Err: cause} }
			case "rename permission":
				ops.rename = func(string, string) error { return cause }
			case "rename other":
				cause = syscall.EIO
				ops.rename = func(string, string) error { return cause }
			case "fallback open":
				ops.rename = func(string, string) error { return syscall.EBUSY }
			}
			ops.openFile = func(string, int, os.FileMode) (configOutput, error) {
				if stage != "fallback open" {
					t.Fatal("must not truncate on permission or generic rename error")
				}
				return nil, cause
			}
			err := writeConfigFileWithOps(path, []byte("new"), ops)
			if !errors.Is(err, cause) {
				t.Fatalf("error = %v", err)
			}
			if errors.Is(cause, os.ErrPermission) {
				for _, hint := range []string{dir, "父目录", "/app/config", "PUID/PGID"} {
					if !strings.Contains(err.Error(), hint) {
						t.Fatalf("missing diagnostic %q: %v", hint, err)
					}
				}
			}
			got, _ := os.ReadFile(path)
			if string(got) != "original" {
				t.Fatalf("original changed: %s", got)
			}
			files, _ := os.ReadDir(dir)
			if len(files) != 1 {
				t.Fatalf("temporary files leaked: %v", files)
			}
		})
	}
}

type failingConfigOutput struct {
	*os.File
	failure string
}

func (f failingConfigOutput) Write(b []byte) (int, error) {
	if f.failure == "write" {
		n, _ := f.File.Write(b[:1])
		return n, syscall.ENOSPC
	}
	if f.failure == "short" {
		return f.File.Write(b[:1])
	}
	return f.File.Write(b)
}
func (f failingConfigOutput) Sync() error {
	if f.failure == "sync" {
		return syscall.EIO
	}
	return f.File.Sync()
}
func (f failingConfigOutput) Close() error {
	err := f.File.Close()
	if f.failure == "close" {
		return syscall.EIO
	}
	return err
}

func TestConfigWriteBindMountFallback(t *testing.T) {
	for _, failure := range []string{"", "write", "short", "sync", "close"} {
		t.Run("failure="+failure, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.json")
			if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
				t.Fatal(err)
			}
			ops := testConfigOps()
			ops.rename = func(string, string) error { return &os.LinkError{Op: "rename", Err: syscall.EBUSY} }
			ops.openFile = func(p string, flags int, mode os.FileMode) (configOutput, error) {
				f, err := os.OpenFile(p, flags, mode)
				return failingConfigOutput{f, failure}, err
			}
			err := writeConfigFileWithOps(path, []byte("complete new config"), ops)
			files, _ := os.ReadDir(dir)
			if failure == "" {
				if err != nil {
					t.Fatal(err)
				}
				if len(files) != 1 {
					t.Fatalf("tmp leaked: %v", files)
				}
				got, _ := os.ReadFile(path)
				if string(got) != "complete new config" {
					t.Fatalf("config = %s", got)
				}
				return
			}
			if err == nil || len(files) != 2 {
				t.Fatalf("must report failure and keep complete temporary copy: %v, %v", err, files)
			}
			for _, file := range files {
				if file.Name() == "config.json" {
					continue
				}
				tmp := filepath.Join(dir, file.Name())
				got, _ := os.ReadFile(tmp)
				if string(got) != "complete new config" || !strings.Contains(err.Error(), tmp) {
					t.Fatalf("recovery copy/diagnostic: %s, %v", got, err)
				}
			}
		})
	}
}

func TestSaveConfigConcurrentPreservesKeysAndLiveSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"custom":{"untouched":"keep"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	live := livecfg.New(livecfg.Snapshot{})
	p := pool.New("")
	defer p.Close()
	up := upstream.New()
	sch := scheduler.New(scheduler.Config{})
	const count = 32
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			raw := []byte(fmt.Sprintf(`{"api_key":"key-%d","custom":{"key%d":%d}}`, i, i, i))
			if _, err := saveConfig(raw, path, live, p, up, sch); err != nil {
				t.Errorf("save: %v", err)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	custom := got["custom"].(map[string]any)
	if custom["untouched"] != "keep" || len(custom) != count+1 {
		t.Fatalf("lost unknown/concurrent keys: %v", custom)
	}
	if got["api_key"] != live.Load().APIKey {
		t.Fatalf("disk/live disagree: %v / %v", got["api_key"], live.Load().APIKey)
	}
}
