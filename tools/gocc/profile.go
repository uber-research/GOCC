//Copyright (c) 2020 Uber Technologies, Inc.
//
//Licensed under the Uber Non-Commercial License (the "License");
//you may not use this file except in compliance with the License.
//You may obtain a copy of the License at the root directory of this project.
//
//See the License for the specific language governing permissions and
//limitations under the License.
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/tools/go/ssa"
)

// this flag indicates whether profile is provided or not
// and a map to record the name if profile exists
var _profileProvided bool = false
var _hotFuncMap map[string]empty = map[string]empty{}

// add paired lock/unlock position in lockInfo
func isHotFunction(f *ssa.Function) bool {
	pkgPath := f.Pkg.Pkg.Path()
	splits := strings.Split(normalizeFunctionName(pkgPath), "/")
	canonicalName := splits[len(splits)-1]
	funcName := normalizeFunctionName(f.RelString(f.Pkg.Pkg))
	fullFuncName := canonicalName + "." + funcName

	if !_profileProvided {
		return true
	}

	if _, ok := _hotFuncMap[fullFuncName]; !ok {
		return false
	}
	return true
}

func normalizeFunctionName(name string) string {
	// Normalize function names as follows:
	//    A.B.(*C).f -> A.B.C.f
	//    (A.B.C).f -> A.B.C.f
	fName := strings.TrimSpace(name)
	idx := strings.Index(fName, "(*")
	if idx != -1 {
		fName = fName[:idx] + fName[idx+2:]
	}
	idx = strings.Index(fName, "(")
	if idx != -1 {
		fName = fName[:idx] + fName[idx+1:]
	}
	idx = strings.Index(fName, ")")
	if idx != -1 {
		fName = fName[:idx] + fName[idx+1:]
	}
	return fName
}

func initProfile(profilePath string) {
	_profileProvided = true
	f, err := os.Open(profilePath)
	defer f.Close()
	if err != nil {
		panic("cannot open profile")
	}
	rd := bufio.NewReader(f)
	for {
		line, err := rd.ReadString('\n')
		if err == io.EOF {
			fmt.Print(line)
			break
		}
		if err != nil {
			panic("profile is wrong")
		}
		_hotFuncMap[strings.TrimSpace(line)] = emptyStruct
	}
}
