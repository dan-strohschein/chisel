package main

import "fmt"

type QueryEngine struct {
	maxDepth int
}

func NewQueryEngine(depth int) *QueryEngine {
	return &QueryEngine{maxDepth: depth}
}

func (qe *QueryEngine) ProcessRequest(req string) string {
	if !ValidateInput(req) {
		return ""
	}
	result := "processed: " + req
	SaveResult(result)
	return result
}

func ValidateInput(input string) bool {
	return input != ""
}

func SaveResult(result string) {
	fmt.Println("saving:", result)
}

func main() {
	engine := NewQueryEngine(10)
	out := engine.ProcessRequest("hello")
	fmt.Println(out)
}
