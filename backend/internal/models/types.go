package models

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
)

// JSONItems 让订单行以 JSON 存进 SQLite。
type JSONItems []Item

func (j JSONItems) Value() (driver.Value, error) {
	if j == nil {
		return "[]", nil
	}
	b, err := json.Marshal(j)
	return string(b), err
}

func (j *JSONItems) Scan(src any) error {
	if src == nil {
		*j = JSONItems{}
		return nil
	}
	var b []byte
	switch v := src.(type) {
	case []byte:
		b = v
	case string:
		b = []byte(v)
	default:
		return errors.New("unsupported type for JSONItems")
	}
	if len(b) == 0 {
		*j = JSONItems{}
		return nil
	}
	return json.Unmarshal(b, j)
}
