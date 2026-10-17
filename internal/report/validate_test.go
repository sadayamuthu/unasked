package report

import (
	"fmt"
	"math"
)

// validate is a tiny JSON Schema checker: type, required, enum, properties
// and items. Unknown keywords are ignored, and extra properties are allowed
// because 1.x may add fields.
func validate(schema map[string]any, doc any, path string) []string {
	var errs []string
	if t, ok := schema["type"]; ok {
		var types []string
		switch tv := t.(type) {
		case string:
			types = []string{tv}
		case []any:
			for _, x := range tv {
				types = append(types, x.(string))
			}
		}
		okType := false
		for _, ty := range types {
			if typeIs(ty, doc) {
				okType = true
			}
		}
		if !okType {
			return append(errs, fmt.Sprintf("%s: %T is not %v", path, doc, types))
		}
	}
	if en, ok := schema["enum"].([]any); ok {
		found := false
		for _, e := range en {
			if e == doc {
				found = true
			}
		}
		if !found {
			errs = append(errs, fmt.Sprintf("%s: %v not in enum %v", path, doc, en))
		}
	}
	if obj, ok := doc.(map[string]any); ok {
		if req, ok := schema["required"].([]any); ok {
			for _, k := range req {
				if _, has := obj[k.(string)]; !has {
					errs = append(errs, fmt.Sprintf("%s: missing required %q", path, k))
				}
			}
		}
		if props, ok := schema["properties"].(map[string]any); ok {
			for k, sub := range props {
				if v, has := obj[k]; has {
					errs = append(errs, validate(sub.(map[string]any), v, path+"."+k)...)
				}
			}
		}
	}
	if arr, ok := doc.([]any); ok {
		if items, ok := schema["items"].(map[string]any); ok {
			for i, v := range arr {
				errs = append(errs, validate(items, v, fmt.Sprintf("%s[%d]", path, i))...)
			}
		}
	}
	return errs
}

func typeIs(t string, v any) bool {
	switch t {
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "string":
		_, ok := v.(string)
		return ok
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "null":
		return v == nil
	case "number":
		_, ok := v.(float64)
		return ok
	case "integer":
		f, ok := v.(float64)
		return ok && f == math.Trunc(f)
	}
	return false
}
