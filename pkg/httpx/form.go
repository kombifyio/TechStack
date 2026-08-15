package httpx

import (
	"fmt"
	"net/url"
	"reflect"
	"strconv"
)

// bindFormValues unmarshals url.Values (from form-data or x-www-form-urlencoded
// bodies) into dst. dst must be a pointer to a struct or a map[string]any.
//
// It is a deliberately small reflection binder covering the scalar field kinds
// the TechStack form routes use. Fields are matched by the "form" struct tag,
// falling back to the field name. This mirrors the practical subset of
// PocketBase's router.UnmarshalRequestData; complex/nested form binding is out
// of scope because no TechStack route relies on it (JSON is the norm).
func bindFormValues(values url.Values, dst any) error {
	if m, ok := dst.(*map[string]any); ok {
		out := make(map[string]any, len(values))
		for k, v := range values {
			if len(v) == 1 {
				out[k] = v[0]
			} else {
				out[k] = v
			}
		}
		*m = out
		return nil
	}

	rv := reflect.ValueOf(dst)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return fmt.Errorf("httpx: form bind target must be a non-nil pointer, got %T", dst)
	}
	elem := rv.Elem()
	if elem.Kind() != reflect.Struct {
		return fmt.Errorf("httpx: form bind target must point to a struct, got %T", dst)
	}

	t := elem.Type()
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		name := field.Tag.Get("form")
		if name == "" || name == "-" {
			name = field.Name
		}
		raw, ok := values[name]
		if !ok || len(raw) == 0 {
			continue
		}
		if err := setScalar(elem.Field(i), raw); err != nil {
			return fmt.Errorf("httpx: field %q: %w", name, err)
		}
	}
	return nil
}

func setScalar(fv reflect.Value, raw []string) error {
	if fv.Kind() == reflect.Slice && fv.Type().Elem().Kind() == reflect.String {
		fv.Set(reflect.ValueOf(raw))
		return nil
	}
	v := raw[0]
	switch fv.Kind() {
	case reflect.String:
		fv.SetString(v)
	case reflect.Bool:
		b, err := strconv.ParseBool(v)
		if err != nil {
			return err
		}
		fv.SetBool(b)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return err
		}
		fv.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			return err
		}
		fv.SetUint(n)
	case reflect.Float32, reflect.Float64:
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return err
		}
		fv.SetFloat(f)
	default:
		return fmt.Errorf("unsupported field kind %s", fv.Kind())
	}
	return nil
}
