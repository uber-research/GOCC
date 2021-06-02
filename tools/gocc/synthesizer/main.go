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
	"flag"
	"fmt"
	"go/ast"
	"go/types"
	"os"
	"strings"

	libbuilder "github.com/uber-research/GOCC/lib/builder"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// TODO: the goal of this function is to return a safe variable name that can be used in Go program.
// TODO: use foo+number of occurance
// Currently this is not needed, unless the package path contains some weird character...
func safeVariableName(oldName string) string {
	newPkgName := strings.ReplaceAll(oldName, ".", "_")
	newPkgName = strings.ReplaceAll(newPkgName, "/", "_")
	newPkgName = strings.ReplaceAll(newPkgName, "-", "_")
	return newPkgName
}

func main() {
	// command-line argument
	var inputFile string
	flag.StringVar(&inputFile, "input", "", "source dir to analyze")

	flag.Parse()

	if inputFile == "" {
		fmt.Println("Please provide the input!")
		flag.PrintDefaults()
		os.Exit(1)
	}
	var err error
	outputPath, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	outputPath += "/"

	var pkgs []*packages.Package

	pkgConfig := &packages.Config{Mode: packages.LoadAllSyntax, Tests: false}
	pkgs, err = packages.Load(pkgConfig, inputFile+"/...")
	if err != nil {
		panic(err)
	}

	prog, ssapkgs := ssautil.AllPackages(pkgs, ssa.GlobalDebug)
	libbuilder.BuildPackages(prog, ssapkgs, false, true)
	//  this is the list holding package function
	var funcList []*ssa.Function = make([]*ssa.Function, 0)
	// this is the list of names that are interface methods
	var methodNameList []string = make([]string, 0)

	packageMap := make(map[*ssa.Function]string, 0)

	newToOldPkgName := make(map[string]string, 0)
	for _, pkg := range ssapkgs {
		if pkg == nil {
			continue
		}
		for _, mem := range pkg.Members {
			switch mem.(type) {
			case *ssa.Function:
				f, _ := mem.(*ssa.Function)
				fileName := prog.Fset.Position(mem.Pos()).Filename
				// we don't want the testing function, since they cannot be called without using testing package
				if !strings.HasSuffix(fileName, "_test.go") && ast.IsExported(f.Name()) {
					funcList = append(funcList, f)
					pkgName := strings.Split(f.Package().String(), " ")[1]
					newPkgName := safeVariableName(pkgName)
					packageMap[f] = newPkgName
					newToOldPkgName[newPkgName] = pkgName
				}

			case *ssa.Type:
				if itfc, ok := mem.Type().Underlying().(*types.Interface); ok {
					fileName := prog.Fset.Position(mem.Pos()).Filename
					pkgName := strings.Split(mem.Package().String(), " ")[1]
					newPkgName := safeVariableName(pkgName)
					newToOldPkgName[newPkgName] = pkgName
					for i := 0; i < itfc.NumMethods(); i++ {
						if !strings.HasSuffix(fileName, "_test.go") && ast.IsExported(mem.Name()) && ast.IsExported(itfc.Method(i).Name()) {
							methodNameList = append(methodNameList, newPkgName+"."+mem.Name()+"."+itfc.Method(i).Name())
						}
					}
				}
			default:
				//nop
			}
		}
	}
	f, err := os.Create(outputPath + "test.go")
	if err != nil {
		panic(err)
	}
	defer f.Close()
	w := bufio.NewWriter(f)

	fmt.Fprintln(w, "package main")
	if len(funcList)+len(methodNameList) != 0 {
		// write to file
		fmt.Fprintln(w, "import (")
		fmt.Fprintln(w, "\"unsafe\"")
		// add import
		for newName, oldName := range newToOldPkgName {
			fmt.Fprintln(w, newName+" \""+oldName+"\"")
		}
		fmt.Fprintln(w, ")")
		fmt.Fprintln(w, "func main() {")
		fmt.Fprintln(w, "listFunc := make([]unsafe.Pointer, 0)")
		for index, f := range funcList {
			fmt.Fprintf(w, "foo%v := (%v.%v)\n", index, packageMap[f], f.Name())
			fmt.Fprintf(w, "listFunc = append(listFunc, unsafe.Pointer(&foo%v))\n", index)
		}
		for index, m := range methodNameList {
			fmt.Fprintf(w, "bar%v := (%v)\n", index, m)
			fmt.Fprintf(w, "listFunc = append(listFunc, unsafe.Pointer(&bar%v))\n", index)
		}
		fmt.Fprintln(w, "}")
		fmt.Fprintln(w, "func OptiLockSyntheticMain(){}")
		w.Flush()
	} else {
		panic("the library doesn't have functions to change!")
	}
}
