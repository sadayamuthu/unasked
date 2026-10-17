package report

import (
	"encoding/json"
	"io"
)

// WriteJSON writes the report as indented JSON v1.
func WriteJSON(w io.Writer, r *Report) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}
