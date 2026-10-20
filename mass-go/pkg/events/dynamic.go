package events

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"unicode/utf8"
)

// CheckDynamicJSON is an explicit limitation of this experimental file encoding.
// It accepts JSON values and Go numeric scalars. Other dynamic Go types fail;
// custom marshalers, struct field omission and byte-to-base64 coercion cannot
// silently change an extension or configuration value.
func CheckDynamicJSON(v any) error { return checkDynamic(v, map[jsonContainer]bool{}) }

// Track only the active path: shared subvalues are legal, cycles are not. Slice
// length distinguishes finite views that share the same backing array.
type jsonContainer struct {
	kind    reflect.Kind
	pointer uintptr
	length  int
}

func checkDynamic(v any, active map[jsonContainer]bool) error {
	value := reflect.ValueOf(v)
	if value.IsValid() && (value.Kind() == reflect.Map || value.Kind() == reflect.Slice) && value.Len() > 0 {
		key := jsonContainer{kind: value.Kind(), pointer: uintptr(value.UnsafePointer()), length: value.Len()}
		if active[key] {
			return errors.New("dynamic JSON contains a cycle")
		}
		active[key] = true
		defer delete(active, key)
	}
	switch x := v.(type) {
	case nil, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return nil
	case string:
		if !utf8.ValidString(x) {
			return errors.New("invalid UTF-8 dynamic string")
		}
		return nil
	case json.Number:
		if x == "" {
			return errors.New("empty JSON number")
		}
		_, err := json.Marshal(x)
		return err
	case float32:
		if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
			return errors.New("non-finite dynamic number")
		}
		return nil
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return errors.New("non-finite dynamic number")
		}
		return nil
	case []any:
		for _, v := range x {
			if err := checkDynamic(v, active); err != nil {
				return err
			}
		}
		return nil
	case map[string]any:
		for k, v := range x {
			if !utf8.ValidString(k) {
				return errors.New("invalid UTF-8 dynamic key")
			}
			if err := checkDynamic(v, active); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unsupported dynamic JSON type %T", v)
	}
}
