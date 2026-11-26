package game

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
)

func TestMessageIDsAreUnique(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate message-ID test source")
	}
	path := filepath.Join(filepath.Dir(testFile), "messages.go")
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	ids := make(map[uint64]string)
	ast.Inspect(file, func(node ast.Node) bool {
		declaration, ok := node.(*ast.GenDecl)
		if !ok || declaration.Tok != token.CONST {
			return true
		}
		for _, rawSpec := range declaration.Specs {
			spec, ok := rawSpec.(*ast.ValueSpec)
			if !ok || len(spec.Names) != len(spec.Values) {
				continue
			}
			for index, name := range spec.Names {
				literal, ok := spec.Values[index].(*ast.BasicLit)
				if !ok || literal.Kind != token.INT {
					continue
				}
				id, parseErr := strconv.ParseUint(literal.Value, 0, 16)
				if parseErr != nil {
					t.Errorf("parse %s message ID %q: %v", name.Name, literal.Value, parseErr)
					continue
				}
				if existing, duplicate := ids[id]; duplicate {
					t.Errorf("message ID 0x%04x is assigned to both %s and %s", id, existing, name.Name)
					continue
				}
				ids[id] = name.Name
			}
		}
		return false
	})
}

func TestCanonicalInventoryMessageIDs(t *testing.T) {
	pairs := map[string][2]uint16{
		"UpgradeItem":  {RequestUpgradeItem, ReplyUpgradeItem},
		"ThrowItem":    {RequestThrowItem, ReplyThrowItem},
		"WPackSubItem": {RequestWPackSubItems, ReplyWPackSubItems},
		"DeletedItem":  {RequestDeletedItems, ReplyDeletedItems},
	}
	want := map[string][2]uint16{
		"UpgradeItem":  {0x140c, 0x140d},
		"ThrowItem":    {0x141e, 0x141f},
		"WPackSubItem": {0x1462, 0x1463},
		"DeletedItem":  {0x1418, 0x1419},
	}
	for name, pair := range pairs {
		if pair != want[name] {
			t.Errorf("%s IDs = %#v, want %#v", name, pair, want[name])
		}
	}
}
