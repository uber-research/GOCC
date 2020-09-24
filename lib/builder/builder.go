//Copyright (c) 2020 Uber Technologies, Inc.
//
//Licensed under the Uber Non-Commercial License (the "License");
//you may not use this file except in compliance with the License.
//You may obtain a copy of the License at the root directory of this project. 
//
//See the License for the specific language governing permissions and
//limitations under the License.

package builder

import (
	"log"

	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
)

// Loads packages from source.
//   * Uses LoadAllSyntax for config.
//   * Loads tests as well.
func LoadPackages(packagePath string) []*packages.Package {
	packageToLoad := packagePath + "/..."
	log.Println("Loading package from source:", packageToLoad)
	pkgConfig := &packages.Config{Mode: packages.LoadAllSyntax, Tests: true}
	pkgs, err := packages.Load(pkgConfig, packageToLoad)
	if err != nil {
		log.Println(err)
	}
	if packages.PrintErrors(pkgs) > 0 {
		log.Println("  WARNING: package contain errors")
	}
	log.Println("          done")
	log.Println("  # packages loaded = ", len(pkgs))
	return pkgs
}

// Builds the given program on the whole or the list of packages based on the given flags.
func BuildPackages(prog *ssa.Program, ssapkgs []*ssa.Package, buildWholeProgram bool, buildSSAPackageInDebugMode bool) {
	log.Println("Building the program / packages...")
	if buildWholeProgram {
		// Use prog.Build() only when you need all dependencies.
		prog.Build()
	} else {
		for _, p := range ssapkgs {
			if p != nil {
				if buildSSAPackageInDebugMode {
					p.SetDebugMode(true)
				}
				p.Build()
			}
		}
	}
	log.Println("          done")
}
