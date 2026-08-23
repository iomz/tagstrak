package main

import (
	"log"
	"os"

	"github.com/iomz/tagstrak/v2/internal/inventory"
	"gopkg.in/alecthomas/kingpin.v2"
)

var (
	app     = kingpin.New("tagjson", "Import legacy CSV tags into a versioned JSON inventory.")
	inFile  = app.Arg("in", "Source CSV containing legacy PC and binary EPC columns.").Required().String()
	outFile = app.Flag("out", "Destination JSON inventory file.").Short('o').Default("tags.json").String()
)

func main() {
	kingpin.MustParse(app.Parse(os.Args[1:]))
	tags, err := inventory.LoadCSV(*inFile, inventory.DefaultLimits())
	if err != nil {
		log.Fatal(err)
	}
	if err := inventory.SaveFile(*outFile, tags, inventory.DefaultLimits()); err != nil {
		log.Fatal(err)
	}
	log.Printf("saved %d tags in %s", len(tags), *outFile)
}
