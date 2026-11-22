package runtime

func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	return Bool(*value)
}

func CloneSessionFeatures(features *SessionFeatures) *SessionFeatures {
	if features == nil {
		return nil
	}
	cloned := *features
	cloned.AgentQuestions = cloneBool(features.AgentQuestions)
	return &cloned
}

// MergeSessionFeatures keeps every omitted feature inherited from defaults.
// Explicit override values replace their corresponding default fields.
func MergeSessionFeatures(defaults, overrides *SessionFeatures) *SessionFeatures {
	if defaults == nil {
		return CloneSessionFeatures(overrides)
	}
	merged := CloneSessionFeatures(defaults)
	if overrides != nil && overrides.AgentQuestions != nil {
		merged.AgentQuestions = cloneBool(overrides.AgentQuestions)
	}
	return merged
}
