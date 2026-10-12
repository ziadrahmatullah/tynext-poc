// Package contracts embeds the MQTT contract files so that every binary in this
// repo validates against the same bytes that are handed to the OT team.
package contracts

import "embed"

// FS holds the device inventory, the JSON Schemas and the example payloads.
//
//go:embed devices.json mqtt/payloads/*.json mqtt/examples/*.json
var FS embed.FS

// Paths of the embedded schemas, relative to FS.
const (
	TelemetrySchemaPath = "mqtt/payloads/telemetry.schema.json"
	StatusSchemaPath    = "mqtt/payloads/status.schema.json"
	AlarmSchemaPath     = "mqtt/payloads/alarm.schema.json"

	DevicesPath = "devices.json"
	ExamplesDir = "mqtt/examples"
)
