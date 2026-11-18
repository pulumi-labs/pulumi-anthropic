package enginetest

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// Each scenario gets an isolated plugin home and backend. The usual installed
// provider binary is used, not an in-process server or a special test binary.
func installPlugin(t *testing.T) (string, string) {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
	home := t.TempDir()
	version := *providerVersion
	if version == "" {
		version = "0.1.0-dev"
	}
	env := []string{"HOME=" + home, "USERPROFILE=" + home, "APPDATA=" + filepath.Join(home, "AppData", "Roaming"), "LOCALAPPDATA=" + filepath.Join(home, "AppData", "Local"), "USER=acceptance", "LOGNAME=acceptance", "PULUMI_HOME=" + home,
		"PULUMI_SKIP_UPDATE_CHECK=true", "PULUMI_DISABLE_AUTOMATIC_PLUGIN_ACQUISITION=true", "PULUMI_IGNORE_AMBIENT_PLUGINS=true"}
	for _, key := range []string{"PATH", "TMPDIR", "TMP", "TEMP", "GOPROXY", "GOSUMDB", "GOPRIVATE", "GONOSUMDB", "GONOPROXY", "GOTOOLCHAIN", "GOMAXPROCS", "CC", "CXX", "CGO_ENABLED", "SDKROOT", "SYSTEMROOT", "SYSTEMDRIVE", "WINDIR", "PATHEXT"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	for _, key := range []string{"GOCACHE", "GOMODCACHE", "GOPATH"} {
		command := exec.Command("go", "env", key)
		output, err := command.Output()
		if err != nil {
			t.Fatal("cannot locate Go cache")
		}
		env = append(env, key+"="+strings.TrimSpace(string(output)))
	}
	pulumi, err := exec.LookPath("pulumi")
	if err != nil {
		t.Fatal("preinstalled Pulumi CLI required")
	}
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	yaml, err := exec.LookPath("pulumi-language-yaml")
	if err != nil {
		resolved, resolveErr := filepath.EvalSymlinks(pulumi)
		if resolveErr != nil {
			t.Fatal("cannot locate YAML runtime")
		}
		yaml = filepath.Join(filepath.Dir(resolved), "pulumi-language-yaml"+suffix)
	}
	command := exec.Command(pulumi, "version")
	command.Env = env
	output, err := command.Output()
	if err != nil || strings.TrimSpace(string(output)) != "v3.274.0" {
		t.Fatal("preinstalled Pulumi CLI v3.274.0 required")
	}
	command = exec.Command("go", "version", "-m", yaml)
	command.Env = env
	output, err = command.Output()
	if err != nil || !bytes.Contains(output, []byte("github.com/pulumi/pulumi-yaml\tv0.0.0-20261102092348-95239c2d85a2")) {
		t.Fatal("preinstalled pinned YAML runtime required")
	}
	if !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9.-]+)?$`).MatchString(version) {
		t.Fatal("invalid provider version")
	}
	binary := filepath.Join(home, "pulumi-resource-anthropic"+suffix)
	command = exec.Command("go", "build", "-ldflags", "-X github.com/pulumi-labs/pulumi-anthropic/provider/pkg/version.Version="+version, "-o", binary, "./provider/cmd/pulumi-resource-anthropic")
	command.Dir, command.Env = root, env
	if err := command.Run(); err != nil {
		t.Fatal("local provider build failed")
	}
	command = exec.Command(pulumi, "plugin", "install", "resource", "anthropic", version, "--file", binary, "--reinstall")
	command.Env = env
	if err := command.Run(); err != nil {
		t.Fatal("local provider install failed")
	}
	// ProgramTest inherits only these safe overrides; never use an ambient Pulumi
	// home, backend, access token, or provider discovery override during acceptance tests.
	t.Setenv("PULUMI_HOME", home)
	t.Setenv("PULUMI_BACKEND_URL", fileURL(t.TempDir()))
	t.Setenv("PULUMI_ACCESS_TOKEN", "")
	t.Setenv("PULUMI_CONFIG", "")
	t.Setenv("PULUMI_CONFIG_PASSPHRASE_FILE", "")
	t.Setenv("PULUMI_DISABLE_AUTOMATIC_PLUGIN_ACQUISITION", "true")
	t.Setenv("PULUMI_IGNORE_AMBIENT_PLUGINS", "true")
	t.Setenv("PULUMI_SKIP_UPDATE_CHECK", "true")
	t.Setenv("PATH", filepath.Dir(yaml)+string(os.PathListSeparator)+os.Getenv("PATH"))
	return home, version
}

func Program(t *testing.T, raw []byte) []byte {
	t.Helper()
	return bytes.ReplaceAll(raw, []byte("TEST_PROVIDER_VERSION"), []byte(*providerVersion))
}
func fileURL(path string) string {
	path = filepath.ToSlash(path)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return "file://" + path
}
