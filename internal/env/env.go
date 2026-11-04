// Package env holds small helpers for running on a Mac outside a shell:
// loading settings from a private config file and finding command-line tools
// when the app that launched us did not read ~/.zshrc.
package env

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ConfigDir is where per-server settings and secrets live, outside any repo:
// $COMMANDCENTER_CONFIG_DIR, else ~/.config/commandcenter.
func ConfigDir() string {
	if d := os.Getenv("COMMANDCENTER_CONFIG_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "commandcenter")
}

// Load reads KEY=VALUE lines from <ConfigDir>/<name>.env. Values already set
// in the environment win. A missing file is fine. Returns the path it tried.
func Load(name string) string {
	path := filepath.Join(ConfigDir(), name+".env")
	LoadFile(path)
	return path
}

// LoadFile reads KEY=VALUE lines (optionally "export KEY=VALUE", # comments).
func LoadFile(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
		if key != "" && os.Getenv(key) == "" {
			os.Setenv(key, value)
		}
	}
}

// WidenPath appends the usual places command-line tools live on a Mac, since
// apps launched from the Dock do not read shell startup files.
func WidenPath() {
	home, _ := os.UserHomeDir()
	extra := []string{
		"/opt/homebrew/bin",
		"/usr/local/bin",
		filepath.Join(home, ".homebrew", "bin"),
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, ".grok", "bin"),
		filepath.Join(home, ".npm-global", "bin"),
		filepath.Join(home, ".bun", "bin"),
		filepath.Join(home, "go", "bin"),
	}
	nvm, _ := filepath.Glob(filepath.Join(home, ".nvm", "versions", "node", "*", "bin"))
	sort.Sort(sort.Reverse(sort.StringSlice(nvm)))
	extra = append(extra, nvm...)

	path := os.Getenv("PATH")
	if path == "" {
		path = "/usr/bin:/bin"
	}
	parts := strings.Split(path, string(os.PathListSeparator))
	have := map[string]bool{}
	for _, p := range parts {
		have[p] = true
	}
	for _, p := range extra {
		if st, err := os.Stat(p); err == nil && st.IsDir() && !have[p] {
			parts = append(parts, p)
			have[p] = true
		}
	}
	os.Setenv("PATH", strings.Join(parts, string(os.PathListSeparator)))
}
