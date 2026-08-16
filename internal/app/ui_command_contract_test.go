package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	apiaggregate "github.com/flidai/leapview/internal/app/api/aggregate"
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
	bindings := apiaggregate.GetAPIGenUIActions()

	actions := map[string]string{}
	boundOperations := map[string]string{}
	contracts := apiaggregate.GetAPIGenOperationContracts()
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
		if contract.Command == nil || contract.Command.UI == nil {
			t.Errorf("UI action %q references command %q without generated UI metadata", binding.ActionID(), binding.OperationID())
		} else if contract.Command.UI.ActionID != binding.ActionID() {
			t.Errorf("UI action %q disagrees with command %q metadata %q", binding.ActionID(), binding.OperationID(), contract.Command.UI.ActionID)
		}
	}

	for operationID, contract := range contracts {
		if contract.Command == nil || contract.Command.UI == nil {
			continue
		}
		if _, ok := boundOperations[operationID]; !ok {
			t.Errorf("generated UI command %q has no typed browser action binding", operationID)
		}
	}
}

func TestUIRequestsCannotBypassTypedCommandHelpers(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	nonCommandAllowlist := map[string]map[string]bool{
		filepath.Clean("internal/admin/ui/page.go"):             {"QueryPost": true, "EventPost": true},
		filepath.Clean("internal/admin/personalsettings/ui.go"): {"QueryPost": true},
		filepath.Clean("internal/dashboard/ui/page.go"):         {"EventPost": true},
		filepath.Clean("internal/project/ui/data_explorer.go"):  {"EventPost": true},
		filepath.Clean("internal/project/ui/develop.go"):        {"QueryPost": true},
		filepath.Clean("internal/project/ui/page.go"):           {"QueryPost": true},
	}
	fset := token.NewFileSet()
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		inspectUICommandBoundaryAST(t, filepath.Clean(relative), file, nonCommandAllowlist)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func inspectUICommandBoundaryAST(t *testing.T, relative string, file *ast.File, nonCommandAllowlist map[string]map[string]bool) {
	t.Helper()
	actionPackages := map[string]bool{}
	commandPackages := map[string]bool{}
	for _, imported := range file.Imports {
		path, err := strconv.Unquote(imported.Path.Value)
		if err != nil {
			continue
		}
		name := filepath.Base(path)
		if imported.Name != nil {
			name = imported.Name.Name
		}
		if path == "github.com/flidai/leapview/internal/platform/web/actions" {
			actionPackages[name] = true
		}
		if path == "github.com/Yacobolo/toolbelt/apigen/runtime/command" {
			commandPackages[name] = true
		}
		if path == "github.com/Yacobolo/toolbelt/apigen/runtime/ui" &&
			!strings.HasSuffix(relative, ".apigen.gen.go") && relative != filepath.Clean("internal/platform/web/uicommand/binding.go") {
			t.Errorf("%s imports the low-level APIGen UI action constructor; consume a generated GenUIAction function", relative)
		}
	}
	ast.Inspect(file, func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.CallExpr:
			selector, ok := value.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			owner, ok := selector.X.(*ast.Ident)
			if !ok {
				return true
			}
			if commandPackages[owner.Name] && selector.Sel.Name == "BeginInvocation" &&
				!strings.HasSuffix(relative, ".apigen.gen.go") && relative != filepath.Clean("internal/app/api/apigenruntime/handler.go") {
				t.Errorf("%s assembles a generic command invocation; use the operation-specific generated BeginGen or ExecuteGen entry point", relative)
			}
			if !actionPackages[owner.Name] {
				return true
			}
			switch selector.Sel.Name {
			case "Post", "Patch", "UncontractedMutation":
				t.Errorf("%s uses untyped UI mutation helper uiactions.%s", relative, selector.Sel.Name)
			case "QueryPost", "EventPost":
				if !nonCommandAllowlist[relative][selector.Sel.Name] {
					t.Errorf("%s uses unbound UI helper uiactions.%s; use a generated command or add a reviewed semantic classification", relative, selector.Sel.Name)
				}
			}
		case *ast.BasicLit:
			if value.Kind != token.STRING || relative == filepath.Clean("internal/platform/web/actions/actions.go") {
				return true
			}
			literal, err := strconv.Unquote(value.Value)
			if err != nil {
				return true
			}
			for _, forbidden := range []string{"@post(", "@patch(", "window.LeapViewCommand.headers("} {
				if strings.Contains(literal, forbidden) {
					t.Errorf("%s authors browser mutation transport directly with %q; use a classified UI action helper", relative, forbidden)
				}
			}
		}
		return true
	})
}
