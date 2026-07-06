package telemetry_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var dottedMsg = regexp.MustCompile(`^[a-z][a-z0-9._-]*$`)

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, parent, dir, "go.mod not found walking up from cwd")
		dir = parent
	}
}

func msgArgIndex(method string) (int, bool) {
	switch method {
	case "Info", "Warn", "Error", "Debug":
		return 0, true
	case "InfoContext", "WarnContext", "ErrorContext", "DebugContext":
		return 1, true
	default:
		return 0, false
	}
}

func TestLogMessages_DottedConvention(t *testing.T) {
	root := moduleRoot(t)
	var violations []string

	for _, sub := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, sub), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			fset := token.NewFileSet()
			f, perr := parser.ParseFile(fset, path, nil, 0)
			if perr != nil {
				return nil
			}
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, ok := sel.X.(*ast.Ident)
				if !ok || pkg.Name != "slog" {
					return true
				}
				idx, ok := msgArgIndex(sel.Sel.Name)
				if !ok || len(call.Args) <= idx {
					return true
				}
				lit, ok := call.Args[idx].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				msg, uerr := strconv.Unquote(lit.Value)
				if uerr != nil || dottedMsg.MatchString(msg) {
					return true
				}
				pos := fset.Position(lit.Pos())
				rel, _ := filepath.Rel(root, path)
				violations = append(violations, fmt.Sprintf("%s:%d %q", rel, pos.Line, msg))
				return true
			})
			return nil
		})
		require.NoError(t, err)
	}

	require.Empty(t, violations, "slog messages must be dotted-event constants (no spaces/prose/colons):\n%s", strings.Join(violations, "\n"))
}
