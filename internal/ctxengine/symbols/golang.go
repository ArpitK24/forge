package symbols

import (
	"go/ast"
	"go/parser"
	"go/token"
)

func extractGo(src []byte) []Symbol {
	fset := token.NewFileSet()
	// SkipObjectResolution keeps parsing cheap; partial ASTs from files
	// with syntax errors are still used.
	f, _ := parser.ParseFile(fset, "", src, parser.SkipObjectResolution|parser.ParseComments)
	if f == nil {
		return nil
	}
	var out []Symbol
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			start := d.Pos()
			if d.Doc != nil {
				start = d.Doc.Pos()
			}
			s := Symbol{
				Name:  d.Name.Name,
				Kind:  KindFunc,
				Start: fset.Position(start).Line,
				End:   fset.Position(d.End()).Line,
			}
			if d.Recv != nil && len(d.Recv.List) > 0 {
				if r := recvName(d.Recv.List[0].Type); r != "" {
					s.Name = r + "." + d.Name.Name
				}
				s.Kind = KindMethod
			}
			out = append(out, s)
		case *ast.GenDecl:
			if d.Tok != token.TYPE {
				continue
			}
			for _, sp := range d.Specs {
				ts, ok := sp.(*ast.TypeSpec)
				if !ok {
					continue
				}
				start, end := ts.Pos(), ts.End()
				// A lone `type X struct{...}` owns its doc comment
				// and the `type` keyword line.
				if len(d.Specs) == 1 {
					start, end = d.Pos(), d.End()
					if d.Doc != nil {
						start = d.Doc.Pos()
					}
				} else if ts.Doc != nil {
					start = ts.Doc.Pos()
				}
				out = append(out, Symbol{
					Name:  ts.Name.Name,
					Kind:  KindType,
					Start: fset.Position(start).Line,
					End:   fset.Position(end).Line,
				})
			}
		}
	}
	return out
}

func recvName(e ast.Expr) string {
	for {
		switch t := e.(type) {
		case *ast.StarExpr:
			e = t.X
		case *ast.ParenExpr:
			e = t.X
		case *ast.IndexExpr:
			e = t.X
		case *ast.IndexListExpr:
			e = t.X
		case *ast.Ident:
			return t.Name
		default:
			return ""
		}
	}
}
