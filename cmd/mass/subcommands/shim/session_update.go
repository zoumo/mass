package shim

import (
	"sort"

	apiruntime "github.com/zoumo/mass/pkg/runtime-spec/api"
	apishim "github.com/zoumo/mass/pkg/shim/api"
)

// buildSessionUpdate converts a RuntimeUpdateEvent into the inputs needed
// for Manager.UpdateSessionMeta the list of changed session fields, a
// reason string, and an apply function that mutates *State.
//
// A single RuntimeUpdateEvent can carry multiple metadata fields; the returned
// apply function handles all non-nil fields in one pass.
// Returns nil changed if the event carries no metadata fields.
func buildSessionUpdate(ev apishim.Event) (changed []string, reason string, apply func(*apiruntime.State)) {
	ru, ok := ev.(apishim.RuntimeUpdateEvent)
	if !ok {
		return nil, "", nil
	}

	var fields []string
	var reasons []string
	var appliers []func(*apiruntime.State)

	if ru.AvailableCommands != nil {
		fields = append(fields, "availableCommands")
		reasons = append(reasons, "commands-updated")
		e := ru.AvailableCommands
		appliers = append(appliers, func(st *apiruntime.State) {
			st.Session.AvailableCommands = convertToStateCommands(e.Commands)
			sortCommandsByName(st.Session.AvailableCommands)
		})
	}
	if ru.ConfigOptions != nil {
		fields = append(fields, "configOptions")
		reasons = append(reasons, "config-updated")
		e := ru.ConfigOptions
		appliers = append(appliers, func(st *apiruntime.State) {
			st.Session.ConfigOptions = convertToStateConfigOptions(e.Options)
			sortConfigOptionsByID(st.Session.ConfigOptions)
		})
	}
	if ru.SessionInfo != nil {
		fields = append(fields, "sessionInfo")
		reasons = append(reasons, "session-info-updated")
		e := *ru.SessionInfo
		appliers = append(appliers, func(st *apiruntime.State) {
			st.Session.SessionInfo = convertToStateSessionInfo(e)
		})
	}
	if ru.CurrentMode != nil {
		fields = append(fields, "currentMode")
		reasons = append(reasons, "mode-updated")
		e := *ru.CurrentMode
		appliers = append(appliers, func(st *apiruntime.State) {
			st.Session.CurrentMode = convertToStateCurrentMode(e)
		})
	}

	if len(fields) == 0 {
		return nil, "", nil
	}

	reason = reasons[0]
	if len(reasons) > 1 {
		reason = "metadata-updated"
	}

	return fields, reason, func(st *apiruntime.State) {
		for _, fn := range appliers {
			fn(st)
		}
	}
}

// ── Convert: apishim → apiruntime AvailableCommands ──────────────────────────

func convertToStateCommands(cmds []apishim.AvailableCommand) []apiruntime.AvailableCommand {
	if len(cmds) == 0 {
		return nil
	}
	out := make([]apiruntime.AvailableCommand, len(cmds))
	for i, c := range cmds {
		out[i] = apiruntime.AvailableCommand{
			Meta:        c.Meta,
			Name:        c.Name,
			Description: c.Description,
			Input:       convertToStateCommandInput(c.Input),
		}
	}
	return out
}

func convertToStateCommandInput(inp *apishim.AvailableCommandInput) *apiruntime.AvailableCommandInput {
	if inp == nil {
		return nil
	}
	if inp.Unstructured != nil {
		return &apiruntime.AvailableCommandInput{
			Unstructured: &apiruntime.UnstructuredCommandInput{
				Meta: inp.Unstructured.Meta,
				Hint: inp.Unstructured.Hint,
			},
		}
	}
	return nil
}

// ── Convert: apishim → apiruntime ConfigOptions ──────────────────────────────

func convertToStateConfigOptions(opts []apishim.ConfigOption) []apiruntime.ConfigOption {
	if len(opts) == 0 {
		return nil
	}
	out := make([]apiruntime.ConfigOption, 0, len(opts))
	for _, o := range opts {
		if o.Select != nil {
			out = append(out, apiruntime.ConfigOption{
				Select: convertToStateConfigOptionSelect(o.Select),
			})
		}
	}
	return out
}

func convertToStateConfigOptionSelect(s *apishim.ConfigOptionSelect) *apiruntime.ConfigOptionSelect {
	return &apiruntime.ConfigOptionSelect{
		Meta:         s.Meta,
		ID:           s.ID,
		Name:         s.Name,
		CurrentValue: s.CurrentValue,
		Description:  s.Description,
		Category:     s.Category,
		Options:      convertToStateConfigSelectOptions(s.Options),
	}
}

func convertToStateConfigSelectOptions(opts apishim.ConfigSelectOptions) apiruntime.ConfigSelectOptions {
	switch {
	case opts.Grouped != nil:
		groups := make([]apiruntime.ConfigSelectGroup, len(opts.Grouped))
		for i, g := range opts.Grouped {
			groups[i] = apiruntime.ConfigSelectGroup{
				Meta:    g.Meta,
				Group:   g.Group,
				Name:    g.Name,
				Options: convertToStateConfigSelectOptionSlice(g.Options),
			}
		}
		return apiruntime.ConfigSelectOptions{Grouped: groups}
	case opts.Ungrouped != nil:
		return apiruntime.ConfigSelectOptions{
			Ungrouped: convertToStateConfigSelectOptionSlice(opts.Ungrouped),
		}
	default:
		return apiruntime.ConfigSelectOptions{}
	}
}

func convertToStateConfigSelectOptionSlice(opts []apishim.ConfigSelectOption) []apiruntime.ConfigSelectOption {
	out := make([]apiruntime.ConfigSelectOption, len(opts))
	for i, o := range opts {
		out[i] = apiruntime.ConfigSelectOption{
			Meta:        o.Meta,
			Name:        o.Name,
			Value:       o.Value,
			Description: o.Description,
		}
	}
	return out
}

// ── Convert: apishim → apiruntime SessionInfo ────────────────────────────────

func convertToStateSessionInfo(e apishim.SessionInfoEvent) *apiruntime.SessionInfo {
	return &apiruntime.SessionInfo{
		Meta:      e.Meta,
		Title:     e.Title,
		UpdatedAt: e.UpdatedAt,
	}
}

// ── Convert: apishim → apiruntime CurrentMode ────────────────────────────────

func convertToStateCurrentMode(e apishim.CurrentModeEvent) *string {
	if e.ModeID == "" {
		return nil
	}
	s := e.ModeID
	return &s
}

// ── Sort helpers ─────────────────────────────────────────────────────────────

// sortCommandsByName sorts a slice of AvailableCommand by Name for deterministic output.
func sortCommandsByName(cmds []apiruntime.AvailableCommand) {
	sort.Slice(cmds, func(i, j int) bool {
		return cmds[i].Name < cmds[j].Name
	})
}

// sortConfigOptionsByID sorts a slice of ConfigOption by Select.ID for deterministic output.
func sortConfigOptionsByID(opts []apiruntime.ConfigOption) {
	sort.Slice(opts, func(i, j int) bool {
		iID := ""
		if opts[i].Select != nil {
			iID = opts[i].Select.ID
		}
		jID := ""
		if opts[j].Select != nil {
			jID = opts[j].Select.ID
		}
		return iID < jID
	})
}
