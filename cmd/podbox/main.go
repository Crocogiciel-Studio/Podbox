package main

import (
	"os"

	"github.com/Crocogiciel-Studio/podbox/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
