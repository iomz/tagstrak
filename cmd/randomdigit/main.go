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

func printDigitString(w io.Writer, i int) {
	fmt.Fprintf(w, "%s\n", binutil.GenerateNLengthDigitString(i))
}

func main() {
	printDigitString(os.Stdout, parseArg(os.Args))
}
