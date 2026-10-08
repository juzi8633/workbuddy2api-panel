package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// Serialize read/merge/write AND hot application so concurrent requests cannot
// lose keys or apply an older snapshot after a newer one. One server owns config.
var configSaveMu sync.Mutex

type configOutput interface {
	io.Writer
	Sync() error
	Close() error
}

// File-system boundary for deterministic offline failure tests.
type configWriteOps struct {
	createTemp func(string, string) (*os.File, error)
	rename     func(string, string) error
	openFile   func(string, int, os.FileMode) (configOutput, error)
}

func writeConfigFile(path string, out []byte) error {
	return writeConfigFileWithOps(path, out, configWriteOps{
		createTemp: os.CreateTemp,
		rename:     os.Rename,
		openFile: func(path string, flags int, mode os.FileMode) (configOutput, error) {
			return os.OpenFile(path, flags, mode)
		},
	})
}

func configWriteError(op, path string, err error) error {
	if errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("%s: %w; 保存配置需要父目录 %q 的写入及遍历权限（仅配置文件可写不够）；Docker 推荐挂载可写配置目录到 /app/config 并使用 -config /app/config/config.json，目录及文件归属应匹配 PUID/PGID；单文件挂载兼容写入还要求目标文件可写", op, err, filepath.Dir(path))
	}
	return fmt.Errorf("%s: %w", op, err)
}

func writeConfigFileWithOps(path string, out []byte, ops configWriteOps) (err error) {
	// Same directory keeps rename atomic; CreateTemp uses a unique name and 0600.
	f, err := ops.createTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return configWriteError("write config (create temporary file)", path, err)
	}
	tmp := f.Name()
	keep := false
	defer func() {
		if !keep {
			if removeErr := os.Remove(tmp); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				err = errors.Join(err, fmt.Errorf("clean config temporary file %q: %w", tmp, removeErr))
			}
		}
	}()
	n, writeErr := f.Write(out)
	if writeErr == nil && n != len(out) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil {
		return configWriteError("write config temporary file", path, writeErr)
	}
	if closeErr != nil {
		return configWriteError("close config temporary file", path, closeErr)
	}
	if renameErr := ops.rename(tmp, path); renameErr != nil {
		// Linux single-file bind mounts reject replacement with EBUSY. Never
		// truncate on permission errors or other rename failures.
		if !errors.Is(renameErr, syscall.EBUSY) {
			return configWriteError("replace config", path, renameErr)
		}
		target, openErr := ops.openFile(path, os.O_WRONLY|os.O_TRUNC, 0600)
		if openErr != nil {
			return configWriteError("replace config (bind mount fallback)", path, openErr)
		}
		n, writeErr = target.Write(out)
		if writeErr == nil && n != len(out) {
			writeErr = io.ErrShortWrite
		}
		if writeErr == nil {
			writeErr = target.Sync()
		}
		closeErr = target.Close()
		if writeErr != nil || closeErr != nil {
			// Truncation has already happened; retain the complete synced new content
			// on write, sync OR close failure and tell the operator where to recover it.
			keep = true
			return configWriteError(fmt.Sprintf("replace config (bind mount fallback; 完整新内容保留在 %s)", tmp), path, errors.Join(writeErr, closeErr))
		}
	}
	return nil
}
