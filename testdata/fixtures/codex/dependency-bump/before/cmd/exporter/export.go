package main

import (
	"encoding/csv"
	"flag"
	"os"
)

func runExport(args []string) error {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	recs, err := loadRecords()
	if err != nil {
		return err
	}
	w := csv.NewWriter(os.Stdout)
	defer w.Flush()
	for _, r := range recs {
		if err := w.Write(r.Fields()); err != nil {
			return err
		}
	}
	return nil
}
