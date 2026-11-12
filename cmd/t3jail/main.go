package main

import (
	"context"
	"fmt"
	"os"

	"github.com/langsjo/t3jail/internal/cli"
)

func main() {
	if err := cli.NewRoot().ExecuteContext(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "t3jail:", err)
		os.Exit(1)
	}
}
