package enginetest

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/pkg/v3/testing/integration"
)

// PreviewOptions runs a copied example with an installed provider and synthetic
// configuration. Updates and refreshes are disabled; any API request fails the test.
func PreviewOptions(t *testing.T, dir string, inputs map[string]string) integration.ProgramTestOptions {
	t.Helper()
	home, _ := installPlugin(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("preview unexpectedly attempted an API request")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(api.Close)
	root, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	backend := fileURL(t.TempDir())
	opts := integration.ProgramTestOptions{
		Dir: root, NoParallel: true, PulumiHomeDir: home, CloudURL: backend,
		Config: inputs, Verbose: true, Stdout: os.Stdout, Stderr: os.Stderr,
		SkipUpdate: true, SkipRefresh: true, SkipExportImport: true, SkipEmptyPreviewUpdate: true,
		Env: []string{"PULUMI_BACKEND_URL=" + backend, "PULUMI_ACCESS_TOKEN=", "PULUMI_CONFIG=", "PULUMI_CONFIG_PASSPHRASE_FILE=",
			"PULUMI_DISABLE_AUTOMATIC_PLUGIN_ACQUISITION=true", "PULUMI_IGNORE_AMBIENT_PLUGINS=true",
			"PULUMI_SKIP_UPDATE_CHECK=true"},
	}
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "ANTHROPIC_") || strings.HasPrefix(name, "ACTIONS_ID_TOKEN_") {
			opts.Env = append(opts.Env, name+"=")
		}
	}
	// Both default and explicit providers receive the loopback-only tripwire.
	opts.Env = append(opts.Env, "ANTHROPIC_BASE_URL="+api.URL)
	return opts
}
