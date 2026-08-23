package main

import (
	"fmt"
	"github.com/iomz/tagstrak/v2/internal/binutil"
	"io"
	"os"
	"strconv"
)

func parseArg(args []string) int {
	if len(args) < 2 {
		panic("insufficient arg")
	}
	i, err := strconv.Atoi(args[1])
	if err != nil {
		panic(err)
	}
	return i
}

func printHexString(w io.Writer, i int) {
	fmt.Fprintf(w, "%s\n", binutil.GenerateNLengthHexString(i))
}

func main() {
	printHexString(os.Stdout, parseArg(os.Args))
}
