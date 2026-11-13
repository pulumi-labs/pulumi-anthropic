// Package enginetest supports explicitly opted-in installed-provider live scenarios.
package enginetest

import (
	"encoding/json"
	"errors"
	"flag"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
	"github.com/pulumi/pulumi/pkg/v3/testing/integration"
)

var (
	live                       = flag.Bool("live", false, "explicitly run the selected live provider scenario; requires separately approved scope")
	liveExamples               = flag.Bool("live-examples", false, "explicitly run the selected live sample program")
	includeBlocked             = flag.Bool("include-blocked", false, "explicitly include selected blocked live scenarios; also requires -live")
	credentialEnv              = flag.String("credential-env", "ANTHROPIC_API_KEY", "selected credential: ANTHROPIC_API_KEY, ANTHROPIC_ADMIN_KEY, ANTHROPIC_ADMIN_OAUTH_KEY, ANTHROPIC_IDENTITY_TOKEN_FILE or ANTHROPIC_IDENTITY_TOKEN_COMMAND")
	workspaceID                = flag.String("workspace-id", "", "workspace scope for the selected live scenario")
	organizationID             = flag.String("organization-id", "", "organization the selected federation rule belongs to")
	federationRuleID           = flag.String("federation-rule-id", "", "federation rule used with the selected identity token source")
	federationServiceAccountID = flag.String("federation-service-account-id", "", "expected federation service-account target")
	providerVersion            = flag.String("provider-version", "0.1.0-dev", "version of the locally built and installed provider")
)

func RequireLive(t *testing.T) {
	t.Helper()
	if !*live {
		t.Skip("live engine scenarios require explicit -live and separately approved scope")
	}
}

// RequireBlockedLive keeps known blocked scenarios out of ordinary live runs.
func RequireBlockedLive(t *testing.T, reason string) {
	t.Helper()
	RequireLive(t)
	if !*includeBlocked {
		t.Skipf("blocked live scenario: %s; investigation requires -live -include-blocked", reason)
	}
}

func RequireLiveExample(t *testing.T) {
	t.Helper()
	if !*liveExamples {
		t.Skip("live examples require explicit -live-examples and separately approved scope")
	}
}

type federationCredentials struct {
	RuleID, IdentityTokenFile, ServiceAccountID, OrganizationID, WorkspaceID string
	IdentityTokenCommand                                                     []string
}

type credentials struct {
	APIKey, AdminKey, AuthToken string
	Federation                  *federationCredentials
}

func (c credentials) properties() map[string]string {
	properties := map[string]string{"apiKey": c.APIKey, "adminKey": c.AdminKey, "authToken": c.AuthToken, "federation": "null"}
	if c.Federation != nil {
		identityToken := map[string]any{"file": c.Federation.IdentityTokenFile}
		if len(c.Federation.IdentityTokenCommand) > 0 {
			identityToken = map[string]any{"command": c.Federation.IdentityTokenCommand}
		}
		raw, _ := json.Marshal(map[string]any{
			"organizationId": c.Federation.OrganizationID, "ruleId": c.Federation.RuleID,
			"identityToken":    identityToken,
			"serviceAccountId": c.Federation.ServiceAccountID,
		})
		properties["federation"] = string(raw)
	}
	return properties
}

func configuredCredentials() (credentials, error) {
	return selectedCredentials(*credentialEnv, os.Getenv(*credentialEnv), federationCredentials{
		RuleID: *federationRuleID, ServiceAccountID: *federationServiceAccountID,
		OrganizationID: *organizationID, WorkspaceID: *workspaceID,
	})
}

func selectedCredentials(env, value string, federation ...federationCredentials) (credentials, error) {
	c := credentials{}
	if value == "" {
		return c, errors.New("selected credential is unavailable")
	}
	switch env {
	case "ANTHROPIC_API_KEY":
		c.APIKey = value
	case "ANTHROPIC_ADMIN_KEY":
		c.AdminKey = value
	case "ANTHROPIC_ADMIN_OAUTH_KEY":
		c.AuthToken = value
	case "ANTHROPIC_IDENTITY_TOKEN_FILE", "ANTHROPIC_IDENTITY_TOKEN_COMMAND":
		if len(federation) != 1 {
			return c, errors.New("federation configuration is required")
		}
		selected := federation[0]
		if selected.RuleID == "" || selected.OrganizationID == "" || selected.WorkspaceID == "" {
			return c, errors.New("federation rule, organization and workspace IDs are required")
		}
		selected.IdentityTokenFile, selected.IdentityTokenCommand = "", nil
		if env == "ANTHROPIC_IDENTITY_TOKEN_FILE" {
			if !filepath.IsAbs(value) {
				return c, errors.New("identity token file must be absolute")
			}
			selected.IdentityTokenFile = value
		} else {
			if err := json.Unmarshal([]byte(value), &selected.IdentityTokenCommand); err != nil || len(selected.IdentityTokenCommand) == 0 || strings.TrimSpace(selected.IdentityTokenCommand[0]) == "" {
				return c, errors.New("identity token command must be a JSON array containing an executable and optional arguments")
			}
		}
		c.Federation = &selected
	default:
		return c, errors.New("unsupported credential-env")
	}
	return c, nil
}

type Context struct {
	config      map[string]string
	credentials credentials
	runName     string
	ids         map[string][]string
}

func (c *Context) RunName() string           { return c.runName }
func (c *Context) Config(name string) string { return c.config[name] }

var configName = regexp.MustCompile("^[A-Za-z][A-Za-z0-9]*$")

func (c *Context) SetConfig(t *testing.T, name, value string) {
	t.Helper()
	if !configName.MatchString(name) || slices.Contains([]string{"apiKey", "adminKey", "authToken", "runName", "organizationId", "workspaceId"}, name) {
		t.Fatal("invalid public scenario config name")
	}
	for _, secret := range c.credentials.properties() {
		if secret != "" && strings.Contains(value, secret) {
			t.Fatal("credential cannot be a public scenario input")
		}
	}
	c.config[name] = value
}

// ResourceID returns an identity actually checkpointed by the engine. No identity
// is inferred from an intercepted response or treated as proof of an API create.
func (c *Context) ResourceID(name string, index ...int) (string, error) {
	ids := c.ids[name]
	n := len(ids) - 1
	if len(index) > 0 {
		n = index[0]
	}
	if n < 0 || n >= len(ids) {
		return "", errors.New("resource identity was not checkpointed; check run name for a possible orphan: " + c.runName)
	}
	return ids[n], nil
}
func (c *Context) ResourceIDs(name string) []string { return slices.Clone(c.ids[name]) }
func (c *Context) observe(stack integration.RuntimeValidationStackInfo) {
	if stack.Deployment == nil {
		return
	}
	for _, state := range stack.Deployment.Resources {
		if state.External || state.Delete || !strings.HasPrefix(string(state.Type), "anthropic:") || state.ID == "" {
			continue
		}
		name, id := state.URN.Name(), string(state.ID)
		if !slices.Contains(c.ids[name], id) {
			c.ids[name] = append(c.ids[name], id)
		}
	}
}
func (c *Context) WorkspaceClient(t *testing.T) sdk.Client { return c.client(t, "workspace") }
func (c *Context) AdminClient(t *testing.T) sdk.Client     { return c.client(t, "admin") }
func (c *Context) OAuthClient(t *testing.T) sdk.Client     { return c.client(t, "oauth") }
func (c *Context) client(t *testing.T, plane string) sdk.Client {
	t.Helper()
	opts := []option.RequestOption{option.WithoutEnvironmentDefaults(), option.WithMaxRetries(0)}
	if plane == "workspace" && *workspaceID != "" && c.credentials.Federation == nil {
		opts = append(opts, option.WithHeader("anthropic-workspace-id", *workspaceID))
	}
	switch {
	case plane == "workspace" && c.credentials.APIKey != "":
		opts = append(opts, option.WithAPIKey(c.credentials.APIKey), option.WithHeader("x-api-key", c.credentials.APIKey), option.WithHeaderDel("Authorization"))
	case plane == "admin" && c.credentials.AdminKey != "":
		opts = append(opts, option.WithAPIKey(c.credentials.AdminKey), option.WithHeader("x-api-key", c.credentials.AdminKey), option.WithHeaderDel("Authorization"))
	case c.credentials.AuthToken != "":
		opts = append(opts, option.WithAuthToken(c.credentials.AuthToken), option.WithMiddleware(oauthBeta))
	case c.credentials.Federation != nil:
		federation := c.credentials.Federation
		opts = append(opts, option.WithFederationTokenProvider(federation.identityTokenProvider(), option.FederationOptions{
			FederationRuleID: federation.RuleID, OrganizationID: federation.OrganizationID,
			WorkspaceID: federation.WorkspaceID, ServiceAccountID: federation.ServiceAccountID,
		}), option.WithMiddleware(oauthBeta))
	default:
		t.Fatal("scenario needs an appropriate " + plane + " credential; select API/admin key or admin OAuth explicitly")
	}
	return sdk.NewClient(opts...)
}

func oauthBeta(r *http.Request, next option.MiddlewareNext) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header = r.Header.Clone()
	beta := r.Header.Get("anthropic-beta")
	if !strings.Contains(","+strings.ReplaceAll(beta, " ", "")+",", ",oauth-2025-04-20,") {
		if beta != "" {
			beta += ","
		}
		r.Header.Set("anthropic-beta", beta+"oauth-2025-04-20")
	}
	return next(r)
}
