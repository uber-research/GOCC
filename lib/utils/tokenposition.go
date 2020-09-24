//Copyright (c) 2020 Uber Technologies, Inc.
//
//Licensed under the Uber Non-Commercial License (the "License");
//you may not use this file except in compliance with the License.
//You may obtain a copy of the License at the root directory of this project.
//
//See the License for the specific language governing permissions and
//limitations under the License.
package utils

import (
	"go/token"

	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
)

func GetPositionFromPackage(pkg *packages.Package, pos token.Pos) token.Position {
	if pkg != nil {
		return pkg.Fset.Position(pos)
	}
	return token.Position{}
}

func GetPositionStringFromPackage(pkg *packages.Package, pos token.Pos) string {
	return GetPositionFromPackage(pkg, pos).String()
}

func GetPositionFromSsaProgram(prog *ssa.Program, pos token.Pos) token.Position {
	if prog != nil {
		return prog.Fset.Position(pos)
	}
	return token.Position{}
}

func GetPositionStringFromSsaProgram(prog *ssa.Program, pos token.Pos) string {
	return GetPositionFromSsaProgram(prog, pos).String()
}

func GetPositionFromSsaFunction(ssaF *ssa.Function, pos token.Pos) token.Position {
	if ssaF != nil && ssaF.Package() != nil {
		return GetPositionFromSsaProgram(ssaF.Package().Prog, pos)
	}
	return token.Position{}
}

func GetPositionStringFromSsaFunction(ssaF *ssa.Function, pos token.Pos) string {
	return GetPositionFromSsaFunction(ssaF, pos).String()
}

func GetPositionOfSsaInstruction(ssaI ssa.Instruction) token.Position {
	if ssaI != nil {
		return GetPositionFromSsaFunction(ssaI.Parent(), ssaI.Pos())
	}
	return token.Position{}
}

func GetPositionStringOfSsaInstruction(ssaI ssa.Instruction) string {
	return GetPositionOfSsaInstruction(ssaI).String()
}
