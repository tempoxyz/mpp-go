package server

import (
	"encoding/json"
	"fmt"
	"io"
	"reflect"
)

// bodyBytes returns the bytes a challenge digest binds for body, the Body of
// VerifyParams or ChargeParams. A []byte, string or json.RawMessage body, or a
// value of a named string or byte-slice type, is digested as is. Any other
// value is digested as its json.Marshal output. An io.Reader, whose JSON form
// says nothing about its content, and a value json.Marshal cannot encode are
// errors.
func bodyBytes(body any) ([]byte, error) {
	switch v := body.(type) {
	case []byte:
		return v, nil
	case string:
		return []byte(v), nil
	case json.RawMessage:
		return v, nil
	case io.Reader:
		return nil, fmt.Errorf("server: unsupported body type %T: read the body and pass its bytes", body)
	}
	if body != nil {
		rv := reflect.ValueOf(body)
		switch {
		case rv.Kind() == reflect.String:
			return []byte(rv.String()), nil
		case rv.Kind() == reflect.Slice && rv.Type().Elem().Kind() == reflect.Uint8:
			return rv.Bytes(), nil
		}
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("server: unsupported body type %T: %w", body, err)
	}
	return b, nil
}
