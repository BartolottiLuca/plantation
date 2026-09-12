package care

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImportsOnlyStdlibAndDomain(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(".", e.Name()), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", e.Name(), err)
		}
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if !allowedImport(path) {
				t.Errorf("%s imports %s — care may import only the standard library and internal/domain", e.Name(), path)
			}
		}
	}
}

func allowedImport(path string) bool {
	if path == "github.com/BartolottiLuca/plantation/internal/domain" {
		return true
	}
	if !strings.Contains(path, ".") {
		return true
	}
	return strings.HasPrefix(path, "math/") ||
		strings.HasPrefix(path, "go/") ||
		strings.HasPrefix(path, "crypto/") ||
		strings.HasPrefix(path, "encoding/") ||
		strings.HasPrefix(path, "text/") ||
		strings.HasPrefix(path, "html/") ||
		strings.HasPrefix(path, "net/") ||
		strings.HasPrefix(path, "path/") ||
		strings.HasPrefix(path, "os/") ||
		strings.HasPrefix(path, "log/") ||
		strings.HasPrefix(path, "testing/")
}
