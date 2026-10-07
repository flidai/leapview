package main

import (
	"context"
	"os"

	"github.com/flidai/leapview/internal/app/cli"
)

func main() {
	os.Exit(cli.Run(context.Background()))
}
