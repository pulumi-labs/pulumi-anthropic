package enginetest

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/pulumi/pulumi/pkg/v3/secrets/passphrase"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource/config"
	"github.com/pulumi/pulumi/sdk/v3/go/common/workspace"
)

// ProgramTest logs config-set arguments, so don't put real keys in opts.Secrets.
// Seed only the copied project's encrypted stack config, using Pulumi's own
// passphrase manager. This library version forces this test passphrase for all
// commands; private ephemeral directories, not the known phrase, are the security
// boundary. No plaintext keys are written. Never publish these checkpoints.
func writeScenarioConfig(root, stack, project string, c *Context) error {
	salt, manager, err := passphrase.NewPassphraseSecretsManager("correct horse battery staple")
	if err != nil {
		return errors.New("config encryption initialization failed")
	}
	values := config.Map{}
	for name, value := range c.credentials.properties() {
		encrypted, err := manager.Encrypter().EncryptValue(context.Background(), value)
		if err != nil {
			return errors.New("config encryption failed")
		}
		values[config.MustMakeKey(project, name)] = config.NewSecureValue(encrypted)
	}
	for name, value := range c.config {
		values[config.MustMakeKey(project, name)] = config.NewValue(value)
	}
	if c.runName != "" {
		values[config.MustMakeKey(project, "runName")] = config.NewValue(c.runName)
	}
	if *workspaceID != "" {
		values[config.MustMakeKey(project, "workspaceId")] = config.NewValue(*workspaceID)
	}
	state := workspace.ProjectStack{EncryptionSalt: salt, Config: values}
	path := filepath.Join(root, "Pulumi."+stack+".yaml")
	if err := state.Save(path); err != nil {
		return errors.New("encrypted scenario config write failed")
	}
	if os.Chmod(path, 0600) != nil {
		return errors.New("private scenario config permissions failed")
	}
	return nil
}
