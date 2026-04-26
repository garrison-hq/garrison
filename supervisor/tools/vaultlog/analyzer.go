// Package vaultlog implements a go/analysis Analyzer that flags slog/fmt/log
// calls whose arguments include a vault.SecretValue (or an .UnsafeBytes()
// call result). Compile-time enforcement of threat-model Rule 6 / SC-410.
//
// Override: a line-level `//nolint:vaultlog` comment suppresses the
// diagnostic on that exact source line. This is how the spawn-path
// env-var injection (T008) and the leak-scan helper (T003) opt out.
package vaultlog

import (
	"go/ast"
	"go/token"
	"go/types"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

// Analyzer is the exported entry point for singlechecker.Main and
// go vet plugin wiring.
var Analyzer = &analysis.Analyzer{
	Name:     "vaultlog",
	Doc:      "flags slog/fmt calls taking vault.SecretValue arguments",
	Run:      run,
	Requires: []*analysis.Analyzer{inspect.Analyzer},
}

const diagMsg = "vault.SecretValue passed to %s; use sv.LogValue() for slog or do not print; see AGENTS.md + threat model Rule 6"

const pkgSlog = "log/slog"

// guardedSlogFuncs are the log/slog package-level functions to watch.
var guardedSlogFuncs = map[string]bool{
	"Info": true, "Warn": true, "Error": true, "Debug": true, "Log": true,
	"InfoContext": true, "WarnContext": true, "ErrorContext": true, "DebugContext": true,
}

// guardedFmtFuncs are the fmt package functions to watch.
var guardedFmtFuncs = map[string]bool{
	"Printf": true, "Sprintf": true, "Fprintf": true, "Errorf": true,
	"Println": true, "Sprint": true, "Sprintln": true,
}

// guardedLogFuncs are the log package functions to watch.
var guardedLogFuncs = map[string]bool{
	"Printf": true, "Println": true, "Print": true,
	"Fatalf": true, "Fatal": true, "Panicf": true, "Panic": true,
}

func run(pass *analysis.Pass) (interface{}, error) {
	insp := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
	nolintLines := buildNolintLines(pass)
	nolinted := makeNolintedChecker(pass, nolintLines)
	insp.Preorder([]ast.Node{(*ast.CallExpr)(nil)}, func(n ast.Node) {
		checkCallExpr(n, pass, nolinted)
	})
	return nil, nil
}

// buildNolintLines scans each file's comments and builds a map from file
// start position to the set of line numbers carrying "nolint:vaultlog".
func buildNolintLines(pass *analysis.Pass) map[token.Pos]map[int]bool {
	nolintLines := map[token.Pos]map[int]bool{}
	for _, f := range pass.Files {
		lines := map[int]bool{}
		for _, cg := range f.Comments {
			for _, c := range cg.List {
				if strings.Contains(c.Text, "nolint:vaultlog") {
					lines[pass.Fset.Position(c.Slash).Line] = true
				}
			}
		}
		nolintLines[f.Pos()] = lines
	}
	return nolintLines
}

// makeNolintedChecker returns a function that reports whether a given token
// position is on a line suppressed by "nolint:vaultlog".
func makeNolintedChecker(pass *analysis.Pass, nolintLines map[token.Pos]map[int]bool) func(token.Pos) bool {
	return func(pos token.Pos) bool {
		p := pass.Fset.Position(pos)
		for _, f := range pass.Files {
			if pass.Fset.Position(f.Pos()).Filename == p.Filename {
				if m, ok := nolintLines[f.Pos()]; ok {
					return m[p.Line]
				}
			}
		}
		return false
	}
}

// checkCallExpr inspects a single call expression and reports any argument
// that is a vault.SecretValue on a non-nolinted line.
func checkCallExpr(n ast.Node, pass *analysis.Pass, nolinted func(token.Pos) bool) {
	call := n.(*ast.CallExpr)
	calleeName, calleePkg := resolveCallee(call, pass)
	if calleeName == "" || !isGuardedCall(calleePkg, calleeName) {
		return
	}
	displayName := calleePkg + "." + calleeName
	for _, arg := range call.Args {
		if isSecretArg(arg, pass) && !nolinted(call.Pos()) {
			pass.Reportf(arg.Pos(), diagMsg, displayName)
		}
	}
}

// resolveCallee returns (funcName, pkgPath) for a call expression if the callee
// is a known package-level function or a method on *slog.Logger.
func resolveCallee(call *ast.CallExpr, pass *analysis.Pass) (name, pkg string) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", ""
	}

	funcName := sel.Sel.Name

	// Case 1: package-level call (slog.Info, fmt.Sprintf, log.Printf …).
	if id, ok := sel.X.(*ast.Ident); ok {
		obj := pass.TypesInfo.ObjectOf(id)
		if obj == nil {
			return "", ""
		}
		if pkgName, ok := obj.(*types.PkgName); ok {
			return funcName, pkgName.Imported().Path()
		}
	}

	// Case 2: method on *slog.Logger (logger.Info, logger.Error, …).
	recvType := pass.TypesInfo.TypeOf(sel.X)
	if recvType != nil {
		if isLoggerType(recvType) {
			return funcName, pkgSlog
		}
	}

	return "", ""
}

// isLoggerType returns true when typ is *slog.Logger or slog.Logger.
func isLoggerType(typ types.Type) bool {
	// Unwrap pointer.
	t := typ
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj.Name() == "Logger" && obj.Pkg() != nil && obj.Pkg().Path() == pkgSlog
}

// isGuardedCall returns true when (pkgPath, funcName) is in the watched set.
func isGuardedCall(pkgPath, funcName string) bool {
	switch pkgPath {
	case pkgSlog:
		return guardedSlogFuncs[funcName]
	case "fmt":
		return guardedFmtFuncs[funcName]
	case "log":
		return guardedLogFuncs[funcName]
	}
	return false
}

// isSecretArg returns true when the argument is a vault.SecretValue or an
// .UnsafeBytes() call (which yields []byte but signals raw secret access).
func isSecretArg(arg ast.Expr, pass *analysis.Pass) bool {
	// Direct vault.SecretValue value.
	if typ := pass.TypesInfo.TypeOf(arg); typ != nil && isSecretValueType(typ) {
		return true
	}
	// .UnsafeBytes() call — the return type is []byte, but the caller is
	// still using raw secret bytes in a printing context.
	if call, ok := arg.(*ast.CallExpr); ok {
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			if sel.Sel.Name == "UnsafeBytes" {
				if rt := pass.TypesInfo.TypeOf(sel.X); rt != nil && isSecretValueType(rt) {
					return true
				}
			}
		}
	}
	return false
}

// isSecretValueType returns true when typ is vault.SecretValue — identified by
// type name "SecretValue" in any package whose path ends with "vault". This
// matches both the real supervisor vault package and the testdata stub package.
func isSecretValueType(typ types.Type) bool {
	named, ok := typ.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj.Name() == "SecretValue" &&
		obj.Pkg() != nil &&
		strings.HasSuffix(obj.Pkg().Path(), "vault")
}
