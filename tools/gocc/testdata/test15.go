package main

import (
	"reflect"

	"github.com/uber-go/tally"
	// tally "github.com/uber-go/tally"
)

func main() {
	// get function pointer
	// var ptr uintptr = reflect.ValueOf(foo).Pointer()
	var ptr uintptr = reflect.ValueOf(tally.NewRootScope).Pointer()

	// fmt.Printf("0x%x", ptr)
}
