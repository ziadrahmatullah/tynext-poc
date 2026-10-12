package contract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/tynext/tynext-poc/contracts"
)

// maxReasonLen caps the text stored in ingest_rejects.reason and in logs.
const maxReasonLen = 500

var schemaPaths = map[Kind]string{
	KindTelemetry: contracts.TelemetrySchemaPath,
	KindStatus:    contracts.StatusSchemaPath,
	KindAlarm:     contracts.AlarmSchemaPath,
}

// Validator checks raw MQTT payloads against the embedded JSON Schemas.
// It is safe for concurrent use once built.
type Validator struct {
	schemas map[Kind]*jsonschema.Schema
}

// NewValidator compiles the three embedded schemas.
func NewValidator() (*Validator, error) {
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()

	ids := make(map[Kind]string, len(schemaPaths))
	for kind, path := range schemaPaths {
		doc, err := readEmbeddedJSON(path)
		if err != nil {
			return nil, err
		}
		obj, ok := doc.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("schema %s is not a JSON object", path)
		}
		id, ok := obj["$id"].(string)
		if !ok || id == "" {
			return nil, fmt.Errorf("schema %s has no $id", path)
		}
		if err := compiler.AddResource(id, doc); err != nil {
			return nil, fmt.Errorf("add schema %s: %w", path, err)
		}
		ids[kind] = id
	}

	v := &Validator{schemas: make(map[Kind]*jsonschema.Schema, len(ids))}
	for kind, id := range ids {
		sch, err := compiler.Compile(id)
		if err != nil {
			return nil, fmt.Errorf("compile schema %s: %w", id, err)
		}
		v.schemas[kind] = sch
	}
	return v, nil
}

// Validate reports whether raw is a well-formed payload of the given kind.
func (v *Validator) Validate(kind Kind, raw []byte) error {
	sch, ok := v.schemas[kind]
	if !ok {
		return fmt.Errorf("no schema for kind %q", kind)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	if err := sch.Validate(doc); err != nil {
		return err
	}
	return nil
}

// ValidateTelemetry validates raw and decodes it in one step.
func (v *Validator) ValidateTelemetry(raw []byte) (Telemetry, error) {
	var t Telemetry
	err := v.validateInto(KindTelemetry, raw, &t)
	return t, err
}

// ValidateStatus validates raw and decodes it in one step.
func (v *Validator) ValidateStatus(raw []byte) (Status, error) {
	var s Status
	err := v.validateInto(KindStatus, raw, &s)
	return s, err
}

// ValidateAlarm validates raw and decodes it in one step.
func (v *Validator) ValidateAlarm(raw []byte) (Alarm, error) {
	var a Alarm
	err := v.validateInto(KindAlarm, raw, &a)
	return a, err
}

func (v *Validator) validateInto(kind Kind, raw []byte, dst any) error {
	if err := v.Validate(kind, raw); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("decode %s payload: %w", kind, err)
	}
	return nil
}

// Reason flattens a validation error into one line short enough to store in
// ingest_rejects.reason and to log without wrecking the output.
func Reason(err error) string {
	if err == nil {
		return ""
	}
	s := strings.Join(strings.Fields(err.Error()), " ")
	if len(s) > maxReasonLen {
		s = s[:maxReasonLen-3] + "..."
	}
	return s
}

func readEmbeddedJSON(path string) (any, error) {
	f, err := contracts.FS.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open embedded %s: %w", path, err)
	}
	defer f.Close()
	doc, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		return nil, fmt.Errorf("parse embedded %s: %w", path, err)
	}
	return doc, nil
}
