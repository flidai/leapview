package app

import (
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	accessuiaction "github.com/flidai/leapview/internal/access/uiaction"
	agentuiaction "github.com/flidai/leapview/internal/agent/uiaction"
	apiaggregate "github.com/flidai/leapview/internal/app/api/aggregate"
	dashboarduiaction "github.com/flidai/leapview/internal/dashboard/uiaction"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
)

func claimUICommands(request *http.Request, bindings ...uicommand.Binding) {
	operations := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		operations = append(operations, binding.OperationID())
	}
	request.Header.Set(uicommand.HeaderOperationID, strings.Join(operations, ","))
}

func TestUICommandBindingsAreExhaustiveAndGenerated(t *testing.T) {
	bindings := append([]uicommand.Binding{}, accessuiaction.Bindings()...)
	bindings = append(bindings, agentuiaction.Bindings()...)
	bindings = append(bindings, dashboarduiaction.Bindings()...)

	actions := map[string]string{}
	boundOperations := map[string]string{}
	contracts := apiaggregate.GetAPIGenCommandRuntimeContracts()
	for _, binding := range bindings {
		if previous, exists := actions[binding.ActionID()]; exists {
			t.Errorf("UI action %q maps to both %q and %q", binding.ActionID(), previous, binding.OperationID())
		}
		actions[binding.ActionID()] = binding.OperationID()
		if previous, exists := boundOperations[binding.OperationID()]; exists {
			t.Errorf("generated command %q has duplicate UI actions %q and %q", binding.OperationID(), previous, binding.ActionID())
		}
		boundOperations[binding.OperationID()] = binding.ActionID()

		contract, ok := contracts[binding.OperationID()]
		if !ok {
			t.Errorf("UI action %q references missing generated command %q", binding.ActionID(), binding.OperationID())
			continue
		}
		if !contract.Exposes(apigencommand.SurfaceUI) {
			t.Errorf("UI action %q references command %q without UI exposure", binding.ActionID(), binding.OperationID())
		}
	}

	for operationID, contract := range contracts {
		if !contract.Exposes(apigencommand.SurfaceUI) {
			continue
		}
		if _, ok := boundOperations[operationID]; !ok {
			t.Errorf("generated UI command %q has no typed browser action binding", operationID)
		}
	}
}

func TestUIRequestsCannotBypassTypedCommandHelpers(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	nonCommandAllowlist := map[string]map[string]int{
		filepath.Clean("internal/admin/ui/page.go"): {
			"uiactions.QueryPost(": 2, "uiactions.EventPost(": 1, "uiactions.UncontractedMutationPatch(": 1,
		},
		filepath.Clean("internal/workspace/ui/page.go"):          {"uiactions.QueryPost(": 1},
		filepath.Clean("internal/workspace/ui/workspace.go"):     {"uiactions.QueryPost(": 3, "uiactions.UncontractedMutationPost(": 1},
		filepath.Clean("internal/workspace/ui/data_explorer.go"): {"uiactions.EventPost(": 1},
		filepath.Clean("internal/dashboard/ui/page.go"):          {"uiactions.EventPost(": 16},
	}
	nonCommandHelpers := []string{"uiactions.QueryPost(", "uiactions.EventPost(", "uiactions.UncontractedMutationPost(", "uiactions.UncontractedMutationPatch("}
	seenNonCommands := map[string]bool{}
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		source := string(contents)
		for _, forbidden := range []string{"uiactions.Post(", "uiactions.Patch("} {
			if strings.Contains(source, forbidden) {
				t.Errorf("%s uses untyped UI mutation helper %s", relative, forbidden)
			}
		}
		if filepath.Clean(relative) != filepath.Clean("internal/platform/web/actions/actions.go") {
			for _, forbidden := range []string{"@post(", "@patch(", "window.LeapViewCommand.headers("} {
				if strings.Contains(source, forbidden) {
					t.Errorf("%s authors browser mutation transport directly with %q; use a classified UI action helper", relative, forbidden)
				}
			}
		}
		classified := false
		for _, helper := range nonCommandHelpers {
			if strings.Contains(source, helper) {
				classified = true
			}
		}
		if classified {
			allowed, ok := nonCommandAllowlist[filepath.Clean(relative)]
			if !ok {
				t.Errorf("%s adds an unbound UI POST; use a typed generated command or update the reviewed classification", relative)
				return nil
			}
			for _, helper := range nonCommandHelpers {
				want := allowed[helper]
				if got := strings.Count(source, helper); got != want {
					t.Errorf("%s has %d uses of %s, want reviewed count %d", relative, got, helper, want)
				}
			}
			seenNonCommands[filepath.Clean(relative)] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for path := range nonCommandAllowlist {
		if !seenNonCommands[path] {
			t.Errorf("reviewed non-command UI boundary %s changed; remove or update its classification", path)
		}
	}
}
