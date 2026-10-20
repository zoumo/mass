package events

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"time"
)

type Truncation struct {
	RetainedBytes int64  `json:"retainedBytes" minimum:"0"`
	OmittedBytes  *int64 `json:"omittedBytes,omitempty" minimum:"0"`
}
type ObservedError struct {
	Code    any    `json:"code,omitempty"`
	Message string `json:"message"`
}
type ToolChunk struct {
	ID      string         `json:"id"`
	Content []ContentBlock `json:"content"`
}

func (ToolChunk) eventType() string { return "agent.tool_call_chunk" }

type PlanEntry struct {
	Content  string `json:"content"`
	Priority string `json:"priority" enum:"low,medium,high"`
	Status   string `json:"status" enum:"pending,in_progress,completed"`
}
type Plan struct {
	PlanID  string      `json:"planId"`
	Entries []PlanEntry `json:"entries"`
}

func (Plan) eventType() string { return "agent.plan" }

type InfoUpdate struct {
	Title     *string    `json:"title,omitempty"`
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
}

func (InfoUpdate) eventType() string { return "session.info_update" }

type ConfigOptionValue struct {
	Value       string `json:"value"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}
type ConfigOptionGroup struct {
	Group   string              `json:"group"`
	Name    string              `json:"name"`
	Options []ConfigOptionValue `json:"options"`
}

// Options holds either []ConfigOptionValue or []ConfigOptionGroup; decoding uses
// the same union and preserves order. CurrentValue is string or bool by Type.
type ConfigOption struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	Category     string `json:"category,omitempty"`
	Type         string `json:"type"`
	CurrentValue any    `json:"currentValue"`
	Options      any    `json:"options,omitempty"`
}

func (o ConfigOption) Validate() error {
	if o.Type == "boolean" {
		if _, ok := o.CurrentValue.(bool); ok && o.Options == nil {
			return nil
		}
	}
	if o.Type == "select" {
		if _, ok := o.CurrentValue.(string); ok {
			switch o.Options.(type) {
			case []ConfigOptionValue, []ConfigOptionGroup:
				return nil
			}
		}
	}
	return errors.New("invalid config option")
}
func (o *ConfigOption) UnmarshalJSON(data []byte) error {
	type fields ConfigOption
	var raw struct {
		ID           string          `json:"id"`
		Name         string          `json:"name"`
		Description  string          `json:"description,omitempty"`
		Category     string          `json:"category,omitempty"`
		Type         string          `json:"type"`
		CurrentValue json.RawMessage `json:"currentValue"`
		Options      json.RawMessage `json:"options"`
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&raw); err != nil {
		return err
	}
	out := ConfigOption{ID: raw.ID, Name: raw.Name, Description: raw.Description, Category: raw.Category, Type: raw.Type}
	if err := json.Unmarshal(raw.CurrentValue, &out.CurrentValue); err != nil {
		return err
	}
	if raw.Options != nil {
		var values []map[string]json.RawMessage
		if err := json.Unmarshal(raw.Options, &values); err != nil {
			return err
		}
		if values == nil {
			return errors.New("null config options")
		}
		var target any
		if len(values) > 0 && values[0]["group"] != nil {
			target = &[]ConfigOptionGroup{}
		} else {
			target = &[]ConfigOptionValue{}
		}
		d = json.NewDecoder(bytes.NewReader(raw.Options))
		d.DisallowUnknownFields()
		if err := d.Decode(target); err != nil {
			return err
		}
		switch v := target.(type) {
		case *[]ConfigOptionValue:
			out.Options = *v
		case *[]ConfigOptionGroup:
			out.Options = *v
		}
	}
	if err := out.Validate(); err != nil {
		return err
	}
	*o = out
	return nil
}

type ConfigOptionUpdate struct {
	ConfigOptions []ConfigOption `json:"configOptions"`
}

func (ConfigOptionUpdate) eventType() string { return "session.config_option_update" }

type AvailableCommand struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}
type AvailableCommandsUpdate struct {
	AvailableCommands []AvailableCommand `json:"availableCommands"`
}

func (AvailableCommandsUpdate) eventType() string { return "session.available_commands_update" }

type UsageCost struct {
	Amount   json.Number `json:"amount"`
	Currency string      `json:"currency"`
}
type UsageUpdate struct {
	Used int64      `json:"used"`
	Size int64      `json:"size"`
	Cost *UsageCost `json:"cost,omitempty"`
}

func (UsageUpdate) eventType() string { return "session.usage_update" }
func oneOf(value string, values ...string) bool {
	for _, v := range values {
		if value == v {
			return true
		}
	}
	return false
}

func jsonInteger(n json.Number) bool {
	v, ok := new(big.Rat).SetString(string(n))
	return ok && v.IsInt()
}
func validateTool(status string, raw *any, content []ContentBlock, duration *float64, output *Output) error {
	if status != "" && !oneOf(status, "pending", "in_progress", "completed", "failed", "declined", "unknown") {
		return errors.New("invalid tool status")
	}
	if raw != nil {
		if err := CheckDynamicJSON(*raw); err != nil {
			return err
		}
	}
	for _, b := range content {
		if err := b.Validate(); err != nil {
			return err
		}
	}
	if duration != nil && (*duration < 0 || math.IsNaN(*duration) || math.IsInf(*duration, 0)) {
		return errors.New("invalid duration")
	}
	if output != nil {
		if !oneOf(output.Availability, "provided", "unavailable") || !oneOf(output.Completeness, "retained", "truncated", "unknown") {
			return errors.New("invalid tool output")
		}
		if t := output.Truncation; t != nil && (t.RetainedBytes < 0 || t.OmittedBytes != nil && *t.OmittedBytes < 0) {
			return errors.New("invalid truncation")
		}
	}
	return nil
}
