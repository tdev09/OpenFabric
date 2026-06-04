//go:build ignore
// +build ignore

// hellowasm.go is the source for the testdata/hello.wasm WASI test fixture.
// To rebuild:
//
//	GOOS=wasip1 GOARCH=wasm go build -o hello.wasm hellowasm.go
package main

import "fmt"

func main() {
	fmt.Print("hello from wasm")
}
