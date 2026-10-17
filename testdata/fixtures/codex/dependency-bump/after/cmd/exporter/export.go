package main

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"os"
)

func runExport(args []string) error {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the records as JSON instead of CSV")
	if err := fs.Parse(args); err != nil {
		return err
	}
	recs, err := loadRecords()
	if err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(recs)
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
