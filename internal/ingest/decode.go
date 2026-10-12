package ingest

import (
	"fmt"
	"time"

	"github.com/tynext/tynext-poc/internal/contract"
)

// Reasons an incoming message is refused. They double as the low-cardinality
// Prometheus label, so the set has to stay small.
const (
	reasonTopic          = "topic"
	reasonUnknownDevice  = "unknown-device"
	reasonClassMismatch  = "class-mismatch"
	reasonSchema         = "schema"
	reasonDeviceMismatch = "deviceid-mismatch"
)

// message is one decoded, accepted payload on its way to the writer.
type message struct {
	topic    contract.Topic
	deviceID string // devices.id
	received time.Time

	telemetry *contract.Telemetry
	status    *contract.Status
	alarm     *contract.Alarm
}

// reject is one refused payload on its way to ingest_rejects.
type reject struct {
	topic    string
	payload  []byte
	kind     string
	reason   string
	detail   string
	received time.Time
}

func (r reject) error() string {
	if r.detail == "" {
		return r.reason
	}
	return fmt.Sprintf("%s: %s", r.reason, r.detail)
}

// decoder validates a raw MQTT message against the contract.
type decoder struct {
	validator *contract.Validator
	// devices maps devices.code to devices.id and its class, so a payload that
	// names a device we do not know about is caught here instead of failing on
	// a foreign key later.
	devices map[string]deviceRef
}

type deviceRef struct {
	id    string
	class contract.Class
}

// decode returns either a message or a reject, never both.
func (d *decoder) decode(topic string, payload []byte, received time.Time) (*message, *reject) {
	refuse := func(kind, reason, detail string) (*message, *reject) {
		return nil, &reject{
			topic: topic, payload: payload, kind: kind,
			reason: reason, detail: detail, received: received,
		}
	}

	parsed, err := contract.ParseTopic(topic)
	if err != nil {
		return refuse("unknown", reasonTopic, err.Error())
	}

	dev, known := d.devices[parsed.DeviceID]
	if !known {
		return refuse(string(parsed.Kind), reasonUnknownDevice,
			fmt.Sprintf("deviceId %q tidak ada di contracts/devices.json", parsed.DeviceID))
	}
	// The topic says which class the device is. If it disagrees with the
	// inventory, the publisher is building topics the wrong way.
	if dev.class != parsed.Class {
		return refuse(string(parsed.Kind), reasonClassMismatch,
			fmt.Sprintf("topic memakai class %q, tapi %s terdaftar sebagai %q", parsed.Class, parsed.DeviceID, dev.class))
	}

	msg := &message{topic: parsed, deviceID: dev.id, received: received}

	switch parsed.Kind {
	case contract.KindTelemetry:
		tel, err := d.validator.ValidateTelemetry(payload)
		if err != nil {
			return refuse(string(parsed.Kind), reasonSchema, contract.Reason(err))
		}
		if tel.DeviceID != parsed.DeviceID {
			return refuse(string(parsed.Kind), reasonDeviceMismatch,
				fmt.Sprintf("topic %q vs payload %q", parsed.DeviceID, tel.DeviceID))
		}
		msg.telemetry = &tel

	case contract.KindStatus:
		st, err := d.validator.ValidateStatus(payload)
		if err != nil {
			return refuse(string(parsed.Kind), reasonSchema, contract.Reason(err))
		}
		msg.status = &st

	case contract.KindAlarm:
		al, err := d.validator.ValidateAlarm(payload)
		if err != nil {
			return refuse(string(parsed.Kind), reasonSchema, contract.Reason(err))
		}
		if al.DeviceID != parsed.DeviceID {
			return refuse(string(parsed.Kind), reasonDeviceMismatch,
				fmt.Sprintf("topic %q vs payload %q", parsed.DeviceID, al.DeviceID))
		}
		msg.alarm = &al

	default:
		return refuse(string(parsed.Kind), reasonTopic, fmt.Sprintf("jenis payload %q tidak dikenal", parsed.Kind))
	}

	return msg, nil
}
