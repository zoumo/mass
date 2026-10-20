package transport

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

const maxBody = 1 << 20

// Creation DTOs contain only writable fields; output resource models stay unchanged.
type createMetadata struct {
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}
type runtimeCreate struct {
	Metadata createMetadata       `json:"metadata"`
	Spec     resource.RuntimeSpec `json:"spec"`
}
type sessionCreate struct {
	Metadata createMetadata       `json:"metadata"`
	Spec     resource.SessionSpec `json:"spec"`
}

func readBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(10 * time.Second))
	complete := false
	defer func() {
		if complete {
			_ = rc.SetReadDeadline(time.Time{})
		} else {
			_ = rc.SetReadDeadline(time.Now())
			_ = r.Body.Close()
		}
	}()
	b, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	complete = err == nil && len(b) <= maxBody
	if len(b) > maxBody {
		return nil, &protocolError{413, &Error{Code: "resource_exhausted", Reason: "body_too_large", Message: "Request body exceeds capacity"}}
	}
	if err != nil {
		var timeout net.Error
		if errors.As(err, &timeout) && timeout.Timeout() {
			return nil, &protocolError{504, &Error{Code: "deadline_exceeded", Reason: "body_read_timeout", Message: "Request body was not received before the deadline"}}
		}
		return nil, Invalid()
	}
	if len(b) > 0 {
		media, params, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if e != nil || media != "application/json" || (params["charset"] != "" && !strings.EqualFold(params["charset"], "utf-8")) {
			return nil, &protocolError{415, Invalid()}
		}
	}
	if !utf8.Valid(b) {
		return nil, Invalid()
	}
	return b, nil
}

func emptyBody(w http.ResponseWriter, r *http.Request) error {
	b, err := readBody(w, r)
	if err != nil {
		return err
	}
	if len(b) == 0 {
		return nil
	}
	if err := validateJSON(b); err != nil {
		return err
	}
	if string(bytes.TrimSpace(b)) != "{}" { // whitespace inside an empty object is legal
		var v map[string]json.RawMessage
		if json.Unmarshal(b, &v) != nil || v == nil || len(v) != 0 {
			return Invalid()
		}
	}
	return nil
}

func decodeBody(w http.ResponseWriter, r *http.Request, out any) error {
	b, err := readBody(w, r)
	if err != nil {
		return err
	}
	if err := validateJSON(b); err != nil {
		return err
	}
	var value any
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if d.Decode(&value) != nil {
		return Invalid()
	}
	if err := validateShape(value, reflect.TypeOf(out).Elem()); err != nil {
		return err
	}
	d = json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	d.DisallowUnknownFields()
	if d.Decode(out) != nil {
		return Invalid()
	}
	return nil
}

// ValidateJSON rejects duplicate members at every depth, including open maps.
func ValidateJSON(b []byte) error { return validateJSON(b) }

func validateJSON(b []byte) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if jsonValue(d, 0) != nil {
		return Invalid()
	}
	if _, err := d.Token(); err != io.EOF {
		return Invalid()
	}
	return nil
}

func jsonValue(d *json.Decoder, depth int) error {
	if depth > 128 {
		return Invalid()
	}
	token, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			s, ok := key.(string)
			if !ok || seen[s] {
				return Invalid()
			}
			seen[s] = true
			if err := jsonValue(d, depth+1); err != nil {
				return err
			}
		}
		end, err := d.Token()
		if err != nil || end != json.Delim('}') {
			return Invalid()
		}
	case '[':
		for d.More() {
			if err := jsonValue(d, depth+1); err != nil {
				return err
			}
		}
		end, err := d.Token()
		if err != nil || end != json.Delim(']') {
			return Invalid()
		}
	default:
		return Invalid()
	}
	return nil
}

// Fixed model members follow JSON tags; maps/interfaces remain explicitly open.
func validateShape(value any, t reflect.Type) error {
	if t.Kind() == reflect.Interface {
		return nil
	}
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if value == nil {
		return Invalid()
	}
	if t.PkgPath() == "github.com/zoumo/mass/mass-go/pkg/runtime" && t.Name() == "ContentBlock" {
		return nil
	} // The union decoder validates its own branch.
	switch t.Kind() {
	case reflect.Struct:
		object, ok := value.(map[string]any)
		if !ok {
			return Invalid()
		}
		fields := map[string]reflect.StructField{}
		modelFields(t, fields)
		for key := range object {
			if _, ok := fields[key]; !ok {
				return Invalid()
			}
		}
		for key, f := range fields {
			v, present := object[key]
			optional := strings.Contains(f.Tag.Get("json"), ",omitempty") || strings.Contains(f.Tag.Get("json"), ",omitzero")
			if !present {
				if !optional {
					return Invalid()
				}
				continue
			}
			if err := validateShape(v, f.Type); err != nil {
				return err
			}
		}
	case reflect.Slice:
		values, ok := value.([]any)
		if !ok {
			return Invalid()
		}
		for _, v := range values {
			if err := validateShape(v, t.Elem()); err != nil {
				return err
			}
		}
	case reflect.Map:
		if _, ok := value.(map[string]any); !ok {
			return Invalid()
		}
	}
	return nil
}

func modelFields(t reflect.Type, out map[string]reflect.StructField) {
	for f := range t.Fields() {
		if f.Anonymous {
			modelFields(f.Type, out)
			continue
		}
		tag := strings.Split(f.Tag.Get("json"), ",")[0]
		if tag != "" && tag != "-" {
			out[tag] = f
		}
	}
}

// Dynamic positions must already be JSON values; encoding/json must not coerce
// language objects, byte slices or custom marshalers into a different value.
func validateOutput(value reflect.Value, depth int) error {
	if !value.IsValid() {
		return nil
	}
	if depth > 128 {
		return errors.New("response nesting exceeds capacity")
	}
	if value.Kind() == reflect.Interface {
		if value.IsNil() {
			return nil
		}
		return runtime.CheckDynamicJSON(value.Interface())
	}
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil
		}
		return validateOutput(value.Elem(), depth+1)
	}
	switch value.Kind() {
	case reflect.Map:
		if value.Type().Elem().Kind() == reflect.Interface {
			dynamic := map[string]any{}
			for _, k := range value.MapKeys() {
				dynamic[k.String()] = value.MapIndex(k).Interface()
			}
			return runtime.CheckDynamicJSON(dynamic)
		}
		for _, k := range value.MapKeys() {
			if err := validateOutput(value.MapIndex(k), depth+1); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		for i := range value.Len() {
			if err := validateOutput(value.Index(i), depth+1); err != nil {
				return err
			}
		}
	case reflect.Struct:
		for i := range value.NumField() {
			if value.Type().Field(i).IsExported() {
				if err := validateOutput(value.Field(i), depth+1); err != nil {
					return err
				}
			}
		}
		if event, ok := value.Interface().(runtime.SessionEvent); ok {
			if err := validateOutput(reflect.ValueOf(event.Payload()), depth+1); err != nil {
				return err
			}
		}
	case reflect.String:
		if !utf8.ValidString(value.String()) {
			return errors.New("invalid response UTF-8")
		}
	}
	return nil
}
