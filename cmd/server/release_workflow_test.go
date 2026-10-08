package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Exercise the exact publish step, not a separate copy of its version logic.
func TestReleaseWorkflowVersionCheck(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("publish step targets Ubuntu and requires GNU grep")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash unavailable")
	}
	raw, err := os.ReadFile("../../.github/workflows/go-binaries.yml")
	if err != nil {
		t.Fatal(err)
	}
	const marker = "      - name: tag 与源码版本一致性断言\n        run: |\n"
	_, rest, found := strings.Cut(string(raw), marker)
	if !found {
		t.Fatal("release version step not found")
	}
	var script strings.Builder
	for _, line := range strings.Split(rest, "\n") {
		if !strings.HasPrefix(line, "          ") {
			break
		}
		script.WriteString(strings.TrimPrefix(line, "          "))
		script.WriteByte('\n')
	}
	for _, tc := range []struct {
		source, tag string
		ok          bool
	}{
		{"1.13.0-wb19", "v1.13.0-wb19", true},
		{"1.13.0-wb19", "v1.13.0-wb18", false},
		{"1.13.0-wb19", "v1.13.0", false},
		{"1.13.0-wb19", "v1.14.0-wb19", false},
		{"1.13.0-panel", "v1.13.0", true},
		{"1.13.0", "v1.13.0", true},
		{"1.13.0-wb19", "vtest-ci", true},
	} {
		t.Run(tc.source+"/"+tc.tag, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, "cmd/server"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "cmd/server/main.go"), []byte("const appVersion = \""+tc.source+"\"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(bash, "-e", "-c", script.String())
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "GITHUB_REF_NAME="+tc.tag)
			out, err := cmd.CombinedOutput()
			if (err == nil) != tc.ok {
				t.Fatalf("source=%s tag=%s want accepted=%v, err=%v output=%s", tc.source, tc.tag, tc.ok, err, out)
			}
		})
	}
}
