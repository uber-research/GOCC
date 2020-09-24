package utils

import (
	"fmt"
	"strconv"
	"strings"
)

// Normalizes static function names.
func NormalizeFunctionName(name string) string {
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

// Normalize function names obtained from runtime profiles.
func NormalizeDynamicFunctionName(name string) string {
	fName := NormalizeFunctionName(name)
	fName = strings.ReplaceAll(fName, "%2e", ".")
	if strings.Contains(fName, ".") {
		// A.B.C.func1.2.3 => A.B.C$1$2$3
		// A.B.C.glob..func1 => A.B.C.init$1
		// A.B.C.init.0.func2 => A.B.C.init#1$2
		splits := strings.Split(fName, ".")
		suffixStr := ""
		lastIndex := len(splits)
		for i := len(splits) - 1; i >= 0; i-- {
			if v, err := strconv.Atoi(splits[i]); err == nil {
				suffixStr = "$" + strconv.Itoa(v) + suffixStr
			} else {
				lastIndex = i + 1
				break
			}
		}
		var suffixInt int
		if _, err := fmt.Sscanf(splits[lastIndex-1], "func%d", &suffixInt); err == nil {
			if lastIndex-3 >= 0 && splits[lastIndex-3] == "init" {
				if v, err := strconv.Atoi(splits[lastIndex-2]); err == nil {
					fName = strings.Join(splits[:lastIndex-2], ".") + "#" + strconv.Itoa(v+1) + "$" + strconv.Itoa(suffixInt) + suffixStr
				} else {
					panic("Invalid format: expected .init.<num>, not found.")
				}
			} else {
				fName = strings.Join(splits[:lastIndex-1], ".") + "$" + strconv.Itoa(suffixInt) + suffixStr
			}
		} else {
			if lastIndex != len(splits) {
				panic("Unexpected error")
			}
		}
		fName = strings.ReplaceAll(fName, ".glob.", ".init")
	}
	return fName
}
