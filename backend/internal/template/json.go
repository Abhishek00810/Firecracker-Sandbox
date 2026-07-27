package template

import "encoding/json"

// jsonMarshalIndent serializes v to indented JSON (used for the small pointer object).
func jsonMarshalIndent(v any) ([]byte, error) {
	return json.MarshalIndent(v, "", "  ")
}

// jsonUnmarshal decodes JSON into v.
func jsonUnmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}
