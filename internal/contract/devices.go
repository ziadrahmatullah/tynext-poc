package contract

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/tynext/tynext-poc/contracts"
)

// Device is one row of contracts/devices.json, which seeds the devices table
// and drives the simulator.
type Device struct {
	Code    string         `json:"code"`
	Class   Class          `json:"class"`
	Name    string         `json:"name"`
	Breaker string         `json:"breaker,omitempty"`
	Section string         `json:"section,omitempty"`
	Source  map[string]any `json:"source,omitempty"`
}

// Topic builds the topic this device publishes on for the given kind.
func (d Device) Topic(kind Kind) Topic {
	return Topic{Class: d.Class, DeviceID: d.Code, Kind: kind}
}

// SourceJSON renders Source for the devices.source jsonb column.
func (d Device) SourceJSON() ([]byte, error) {
	if d.Source == nil {
		return []byte("null"), nil
	}
	return json.Marshal(d.Source)
}

// LoadDevices reads the embedded inventory and checks it against the contract.
func LoadDevices() ([]Device, error) {
	raw, err := contracts.FS.ReadFile(contracts.DevicesPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", contracts.DevicesPath, err)
	}
	var devices []Device
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&devices); err != nil {
		return nil, fmt.Errorf("parse %s: %w", contracts.DevicesPath, err)
	}

	seen := make(map[string]struct{}, len(devices))
	for i, d := range devices {
		if d.Code == "" {
			return nil, fmt.Errorf("device #%d has no code", i)
		}
		if _, dup := seen[d.Code]; dup {
			return nil, fmt.Errorf("device code %q appears twice", d.Code)
		}
		seen[d.Code] = struct{}{}
		if !validClass(d.Class) {
			return nil, fmt.Errorf("device %q has unknown class %q", d.Code, d.Class)
		}
		if d.Name == "" {
			return nil, fmt.Errorf("device %q has no name", d.Code)
		}
	}
	if len(devices) == 0 {
		return nil, fmt.Errorf("%s is empty", contracts.DevicesPath)
	}
	return devices, nil
}

// DeviceByCode indexes a device slice by code.
func DeviceByCode(devices []Device) map[string]Device {
	m := make(map[string]Device, len(devices))
	for _, d := range devices {
		m[d.Code] = d
	}
	return m
}
