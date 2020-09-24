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
	"io/ioutil"
	"log"
	"os"
	"strings"

	graph "github.com/uber-research/GOCC/tools/gocc/internal/graph"
	"github.com/google/pprof/profile"
)

func buildCallGraphFromProfile(profileFile string, opts *graph.Options) (*graph.Graph, error) {
	log.Println("Profile file: ", profileFile)

	log.Println("Reading profile data file...")
	data, err := ioutil.ReadFile(profileFile)
	if err != nil {
		return nil, err
	}
	log.Println("          done")
	log.Println("Byte array len = ", len(data))

	log.Println("Parsing profile data...")
	prof, err := profile.ParseData(data)
	if err != nil {
		return nil, err
	}
	log.Println("          done")
	log.Println("Building callgraph from profile data...")
	graph := graph.New(prof, opts)
	log.Println("          done")
	log.Println("  # of nodes = ", len(graph.Nodes))
	return graph, nil
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

// the goal of this function is to generate a list of hot function, whose duration is more than 1% of total execution time.
// output will be hotfunc.txt
func main() {

	var inputFile string
	flag.StringVar(&inputFile, "input", "", "profile to analyze")

	flag.Parse()

	if inputFile == "" {
		fmt.Println("Please provide the input!")
		flag.PrintDefaults()
		os.Exit(1)
	}

	graph, err := buildCallGraphFromProfile(inputFile, &graph.Options{
		CallTree:    false,
		SampleValue: func(v []int64) int64 { return v[1] },
	})
	if err != nil {
		panic("error building the graph")
	}
	var sum int64
	for _, node := range graph.Nodes {
		sum += node.FlatValue()
	}
	fmt.Println(sum)
	threshold := sum / 100
	fmt.Println(threshold)

	f, err := os.Create("hotfunc.txt")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer f.Close()

	w := bufio.NewWriter(f)

	for _, n := range graph.Nodes {
		if n.CumValue() > threshold {
			splits := strings.Split(normalizeFunctionName(n.Info.Name), "/")
			canonicalName := splits[len(splits)-1]
			fmt.Println(canonicalName)
			fmt.Fprintln(w, canonicalName)
		}
	}
	w.Flush()
}
