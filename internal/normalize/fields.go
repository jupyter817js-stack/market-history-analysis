package normalize

import (
	"encoding/json"
	"strconv"
	"strings"
)

func AsString(v any) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		if t {
			return "true"
		}
		return "false"
	default:
		b, _ := json.Marshal(t)
		return strings.Trim(string(b), `"`)
	}
}

func AsFloatPtr(v any) *float64 {
	if v == nil {
		return nil
	}
	switch t := v.(type) {
	case float64:
		f := t
		return &f
	case json.Number:
		if f, err := t.Float64(); err == nil {
			return &f
		}
	case string:
		if t == "" {
			return nil
		}
		if f, err := strconv.ParseFloat(t, 64); err == nil {
			return &f
		}
	case int:
		f := float64(t)
		return &f
	case int64:
		f := float64(t)
		return &f
	}
	return nil
}

func AsIntPtr(v any) *int {
	if v == nil {
		return nil
	}
	switch t := v.(type) {
	case float64:
		n := int(t)
		return &n
	case json.Number:
		if n, err := t.Int64(); err == nil {
			i := int(n)
			return &i
		}
		if f, err := t.Float64(); err == nil {
			i := int(f)
			return &i
		}
	case int:
		n := t
		return &n
	case int64:
		n := int(t)
		return &n
	case string:
		if t == "" {
			return nil
		}
		if n, err := strconv.Atoi(t); err == nil {
			return &n
		}
	}
	return nil
}

func AsInt64Ptr(v any) *int64 {
	if v == nil {
		return nil
	}
	switch t := v.(type) {
	case float64:
		n := int64(t)
		return &n
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return &n
		}
		if f, err := t.Float64(); err == nil {
			n := int64(f)
			return &n
		}
	case int:
		n := int64(t)
		return &n
	case int64:
		n := t
		return &n
	case string:
		if t == "" {
			return nil
		}
		if n, err := strconv.ParseInt(t, 10, 64); err == nil {
			return &n
		}
	}
	return nil
}

func AsBoolPtr(v any) *bool {
	if v == nil {
		return nil
	}
	switch t := v.(type) {
	case bool:
		b := t
		return &b
	case string:
		switch strings.ToLower(t) {
		case "true", "t", "yes", "1":
			b := true
			return &b
		case "false", "f", "no", "0":
			b := false
			return &b
		}
	case float64:
		b := t != 0
		return &b
	case json.Number:
		if n, err := t.Int64(); err == nil {
			b := n != 0
			return &b
		}
	}
	return nil
}
