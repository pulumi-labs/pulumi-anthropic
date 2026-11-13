package enginetest

import (
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/pkg/v3/engine"
	"github.com/pulumi/pulumi/pkg/v3/testing/integration"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/common/workspace"
)

type Prepare func(*testing.T, *Context) func(*testing.T, integration.RuntimeValidationStackInfo)

func Options(t *testing.T, dir string, prepare ...Prepare) integration.ProgramTestOptions {
	t.Helper()
	RequireLive(t)
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// Reject malformed projects before creating any live prerequisites.
	if _, err := workspace.LoadProject(filepath.Join(root, dir, "Pulumi.yaml")); err != nil {
		t.Fatalf("invalid scenario project: %v", err)
	}
	if !slices.Contains([]string{"ANTHROPIC_API_KEY", "ANTHROPIC_ADMIN_KEY", "ANTHROPIC_ADMIN_OAUTH_KEY", "ANTHROPIC_IDENTITY_TOKEN_FILE", "ANTHROPIC_IDENTITY_TOKEN_COMMAND"}, *credentialEnv) {
		t.Fatal("unsupported credential-env")
	}
	creds, err := configuredCredentials()
	if err != nil {
		t.Fatal(err)
	}
	c := &Context{config: map[string]string{}, credentials: creds, runName: "pulumi-test-" + strings.ToLower(rand.Text()[:12]), ids: map[string][]string{}}
	t.Logf("Live scenario run name: %s", c.runName)
	home, version := installPlugin(t)
	backend := t.TempDir()
	var validate func(*testing.T, integration.RuntimeValidationStackInfo)
	if len(prepare) > 0 && prepare[0] != nil {
		validate = prepare[0](t, c)
	}
	opts := integration.ProgramTestOptions{
		Dir: filepath.Join(root, dir), NoParallel: true, PulumiHomeDir: home, CloudURL: fileURL(backend),
		Env:     []string{"PULUMI_DISABLE_AUTOMATIC_PLUGIN_ACQUISITION=true", "PULUMI_IGNORE_AMBIENT_PLUGINS=true", "PULUMI_CONFIG_PASSPHRASE=correct horse battery staple", "PULUMI_SKIP_UPDATE_CHECK=true", "PULUMI_BACKEND_URL=" + fileURL(backend)},
		Verbose: true, Stdout: os.Stdout, Stderr: os.Stderr,
		ExtraRuntimeValidation: func(t *testing.T, stack integration.RuntimeValidationStackInfo) {
			c.observe(stack)
			assertProvider(t, stack, version, creds)
			if validate != nil {
				validate(t, stack)
			}
		},
	}
	// Explicit encrypted provider config is authoritative; ambient SDK credential,
	// discovery and base-URL variables must not escape into plugin processes.
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "ANTHROPIC_") {
			opts.Env = append(opts.Env, name+"=")
		}
	}
	stackName := string(opts.GetStackName())
	opts.PrePrepareProject = func(project *engine.Projinfo) error {
		t.Cleanup(func() { _ = os.RemoveAll(project.Root) })
		if err := os.Chmod(project.Root, 0700); err != nil {
			return errors.New("private project permissions failed")
		}
		path := filepath.Join(project.Root, "Pulumi.yaml")
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err = os.WriteFile(path, Program(t, raw), 0600); err != nil {
			return err
		}
		return writeScenarioConfig(project.Root, stackName, string(project.Proj.Name), c)
	}
	return opts
}

// ConfigureWorkspaceExample uses the explicitly selected credential for a sample's
// default provider, without exposing credentials in config-set command arguments.
func ConfigureWorkspaceExample(t *testing.T, opts *integration.ProgramTestOptions) {
	t.Helper()
	RequireLiveExample(t)
	creds, err := configuredCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if creds.APIKey == "" && creds.AuthToken == "" && creds.Federation == nil {
		t.Fatal("workspace examples require an API key, OAuth token or federation")
	}
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "ANTHROPIC_") {
			opts.Env = append(opts.Env, name+"=")
		}
	}
	prepare := opts.PrePrepareProject
	stackName := string(opts.GetStackName())
	opts.PrePrepareProject = func(project *engine.Projinfo) error {
		if prepare != nil {
			if err := prepare(project); err != nil {
				return err
			}
		}
		return writeScenarioConfig(project.Root, stackName, "anthropic", &Context{credentials: creds})
	}
}

func assertProvider(t *testing.T, stack integration.RuntimeValidationStackInfo, version string, creds credentials) {
	t.Helper()
	if stack.Deployment == nil {
		t.Fatal("missing checkpoint")
	}
	found := false
	for _, state := range stack.Deployment.Resources {
		if state.Type != "pulumi:providers:anthropic" {
			continue
		}
		found = true
		if state.Inputs["version"] != version {
			t.Error("checkpoint does not identify the selected provider version")
		}
		for name, value := range creds.properties() {
			if value == "" {
				continue
			}
			for _, bag := range []map[string]any{state.Inputs, state.Outputs} {
				secret, ok := bag[name].(map[string]any)
				if !ok || secret[string(resource.SigKey)] != resource.SecretSig {
					t.Errorf("provider %s lacks checkpoint secrecy", name)
				}
			}
		}
	}
	if !found {
		t.Fatal("explicit Anthropic provider missing from checkpoint")
	}
}
