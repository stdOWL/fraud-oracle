//go:build wasip1

package main

import (
	"github.com/smartcontractkit/cre-sdk-go/cre"
	"github.com/smartcontractkit/cre-sdk-go/cre/wasm"
)

// main is the WASM entry point. The runner parses config.<target>.json into Config.
func main() {
	wasm.NewRunner(cre.ParseJSON[Config]).Run(InitWorkflow)
}
