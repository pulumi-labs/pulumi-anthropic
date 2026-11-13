//go:build acceptance

package test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	anthropic "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/pulumi/pulumi/pkg/v3/testing/integration"
)

func independentModels(t *testing.T, client anthropic.Client) []any {
	t.Helper()
	expected := []any{}
	pages := client.Models.ListAutoPaging(context.Background(), anthropic.ModelListParams{})
	for pages.Next() {
		model := pages.Current()
		if model.ID == "" {
			t.Fatal("independent SDK observation lacks usable model identity")
		}
		expected = append(expected, map[string]any{"id": model.ID, "displayName": model.DisplayName, "createdAt": model.CreatedAt.Format(time.RFC3339Nano)})
	}
	if pages.Err() != nil {
		t.Fatal("independent SDK model observation failed (raw diagnostic suppressed)")
	}
	return expected
}

func validateModels(output any, expected []any) error {
	if !reflect.DeepEqual(output, expected) {
		return errors.New("engine models differ from independent SDK observation (IDs, count/order, display names or creation timestamps)")
	}
	return nil
}

func prepareModelsObservation(t *testing.T, c *ScenarioContext) func(*testing.T, integration.RuntimeValidationStackInfo) {
	t.Helper()
	sdk := c.WorkspaceClient(t)
	expected := independentModels(t, sdk)
	return func(t *testing.T, stack integration.RuntimeValidationStackInfo) {
		if err := validateModels(stack.Outputs["models"], expected); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLiveModels(t *testing.T) {
	opts := liveOptions(t, "models", prepareModelsObservation)
	opts.SkipRefresh = true
	integration.ProgramTest(t, &opts)
}
