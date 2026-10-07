package canopee

import (
	"encoding/json"
	"fmt"
	"reflect"
)

// toShape converts a typed value into the codec's serde_json-shaped value:
// structs → map[string]any, byte arrays → []any of numbers, enums → their
// name strings, maps → map[string]any. Field names come from `json` tags.
func toShape(v any) (any, error) {
	return shapeValue(reflect.ValueOf(v))
}

func shapeValue(rv reflect.Value) (any, error) {
	if !rv.IsValid() {
		return nil, nil
	}
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil, nil
		}
		rv = rv.Elem()
	}
	switch rv.Kind() {
	case reflect.Interface:
		if rv.IsNil() {
			return nil, nil
		}
		return shapeValue(rv.Elem())
	case reflect.String:
		return rv.String(), nil
	case reflect.Bool:
		return rv.Bool(), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int(), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return rv.Uint(), nil
	case reflect.Float32, reflect.Float64:
		return rv.Float(), nil
	case reflect.Array, reflect.Slice:
		n := rv.Len()
		out := make([]any, n)
		if rv.Type().Elem().Kind() == reflect.Uint8 {
			for i := 0; i < n; i++ {
				out[i] = rv.Index(i).Uint()
			}
		} else {
			for i := 0; i < n; i++ {
				x, err := shapeValue(rv.Index(i))
				if err != nil {
					return nil, err
				}
				out[i] = x
			}
		}
		return out, nil
	case reflect.Map:
		out := make(map[string]any, rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			key := fmt.Sprint(iter.Key())
			val, err := shapeValue(iter.Value())
			if err != nil {
				return nil, err
			}
			out[key] = val
		}
		return out, nil
	case reflect.Struct:
		t := rv.Type()
		out := make(map[string]any, t.NumField())
		for i := 0; i < t.NumField(); i++ {
			name := t.Field(i).Tag.Get("json")
			if name == "" {
				name = t.Field(i).Name
			}
			if name == "-" {
				continue
			}
			if fv := rv.Field(i); fv.CanInterface() {
				x, err := shapeValue(fv)
				if err != nil {
					return nil, err
				}
				out[name] = x
			}
		}
		return out, nil
	default:
		return nil, fmt.Errorf("toShape: unsupported kind %s", rv.Kind())
	}
}

// fromShape converts a codec value shape back into a typed value. Shapes are
// serde_json-shaped (keys = wire names, bytes = number arrays), so a plain
// JSON round-trip into the tagged struct is exact.
func fromShape(shape any, dst any) error {
	b, err := json.Marshal(shape)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, dst)
}
