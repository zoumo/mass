package jsonrpc

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"unicode/utf8"
)

// ErrLossyJSON reports that a Go value has no lossless JSON representation.
var ErrLossyJSON = errors.New("JSON value cannot be represented losslessly")

var rawMessageType = reflect.TypeFor[json.RawMessage]()

type losslessVisit struct {
	typ  reflect.Type
	kind reflect.Kind
	ptr  uintptr
	len  int
}

// ValidateLossless rejects string values that encoding/json would replace or
// reinterpret instead of preserving the caller's value.
func ValidateLossless(value any) error {
	return validateLossless(reflect.ValueOf(value), map[losslessVisit]struct{}{})
}

func validateLossless(value reflect.Value, seen map[losslessVisit]struct{}) error {
	if !value.IsValid() {
		return nil
	}

	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return nil
		}
		return validateLossless(value.Elem(), seen)
	case reflect.Pointer:
		if value.IsNil() {
			return nil
		}
		if alreadyVisited(value, seen) {
			return nil
		}
		return validateLossless(value.Elem(), seen)
	case reflect.String:
		if !utf8.ValidString(value.String()) {
			return fmt.Errorf("%w: invalid UTF-8 string", ErrLossyJSON)
		}
		return nil
	case reflect.Slice:
		if value.IsNil() {
			return nil
		}
		if value.Type() == rawMessageType {
			data := value.Bytes()
			if !utf8.Valid(data) || !validJSONStringEscapes(data) {
				return fmt.Errorf("%w: invalid JSON raw message", ErrLossyJSON)
			}
			return nil
		}
		if value.Type().Elem().Kind() == reflect.Uint8 {
			return nil
		}
		if alreadyVisited(value, seen) {
			return nil
		}
		for i := range value.Len() {
			if err := validateLossless(value.Index(i), seen); err != nil {
				return err
			}
		}
		return nil
	case reflect.Array:
		for i := range value.Len() {
			if err := validateLossless(value.Index(i), seen); err != nil {
				return err
			}
		}
		return nil
	case reflect.Map:
		if value.IsNil() {
			return nil
		}
		if alreadyVisited(value, seen) {
			return nil
		}
		iter := value.MapRange()
		for iter.Next() {
			if err := validateLossless(iter.Key(), seen); err != nil {
				return err
			}
			if err := validateLossless(iter.Value(), seen); err != nil {
				return err
			}
		}
		return nil
	case reflect.Struct:
		typ := value.Type()
		for i := range value.NumField() {
			field := typ.Field(i)
			if field.PkgPath != "" || field.Tag.Get("json") == "-" {
				continue
			}
			if err := validateLossless(value.Field(i), seen); err != nil {
				return err
			}
		}
	}
	return nil
}

func alreadyVisited(value reflect.Value, seen map[losslessVisit]struct{}) bool {
	visit := losslessVisit{typ: value.Type(), kind: value.Kind(), ptr: value.Pointer()}
	if value.Kind() == reflect.Slice {
		visit.len = value.Len()
	}
	if _, ok := seen[visit]; ok {
		return true
	}
	seen[visit] = struct{}{}
	return false
}
