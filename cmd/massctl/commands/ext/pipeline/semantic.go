package pipeline

import "fmt"

type ValidationError struct {
	Path    string
	Message string
}

func (e ValidationError) Error() string {
	if e.Path != "" {
		return fmt.Sprintf("%s: %s", e.Path, e.Message)
	}
	return e.Message
}

func ValidateSemantic(p *Pipeline) []ValidationError {
	var errs []ValidationError
	errs = append(errs, checkStageNameUniqueness(p.Stages)...)
	errs = append(errs, checkReservedStageNames(p.Stages)...)
	errs = append(errs, checkGotoTargets(p.Stages)...)
	errs = append(errs, checkAgentRunRefs(p)...)
	errs = append(errs, checkInputFromRefs(p.Stages)...)
	errs = append(errs, checkReachability(p.Stages)...)
	return errs
}

func checkStageNameUniqueness(stages []Stage) []ValidationError {
	var errs []ValidationError
	seen := make(map[string]int)
	for i, s := range stages {
		if prev, ok := seen[s.Name]; ok {
			errs = append(errs, ValidationError{
				Path:    fmt.Sprintf("stages[%d].name", i),
				Message: fmt.Sprintf("duplicate stage name %q (first defined at stages[%d])", s.Name, prev),
			})
		}
		seen[s.Name] = i
	}
	return errs
}

func checkReservedStageNames(stages []Stage) []ValidationError {
	var errs []ValidationError
	for i, s := range stages {
		if s.Name == "__done__" || s.Name == "__escalate__" {
			errs = append(errs, ValidationError{
				Path:    fmt.Sprintf("stages[%d].name", i),
				Message: fmt.Sprintf("%q is a reserved name and cannot be used as a stage name", s.Name),
			})
		}
	}
	return errs
}

func checkGotoTargets(stages []Stage) []ValidationError {
	var errs []ValidationError
	stageNames := make(map[string]bool)
	for _, s := range stages {
		stageNames[s.Name] = true
	}
	validTargets := map[string]bool{"__done__": true, "__escalate__": true}

	for i, s := range stages {
		for j, r := range s.Routes {
			if !stageNames[r.Goto] && !validTargets[r.Goto] {
				errs = append(errs, ValidationError{
					Path:    fmt.Sprintf("stages[%d].routes[%d].goto", i, j),
					Message: fmt.Sprintf("goto target %q is not a known stage, __done__, or __escalate__", r.Goto),
				})
			}
		}
	}
	return errs
}

func checkAgentRunRefs(p *Pipeline) []ValidationError {
	var errs []ValidationError
	for i, s := range p.Stages {
		stageType := s.Type
		if stageType == "" {
			stageType = "serial"
		}
		if stageType == "parallel" {
			for j, t := range s.Tasks {
				if _, ok := p.AgentRuns[t.AgentRun]; !ok {
					errs = append(errs, ValidationError{
						Path:    fmt.Sprintf("stages[%d].tasks[%d].agentRun", i, j),
						Message: fmt.Sprintf("agentRun %q not defined in agentRuns", t.AgentRun),
					})
				}
			}
		} else if s.AgentRun != "" {
			if _, ok := p.AgentRuns[s.AgentRun]; !ok {
				errs = append(errs, ValidationError{
					Path:    fmt.Sprintf("stages[%d].agentRun", i),
					Message: fmt.Sprintf("agentRun %q not defined in agentRuns", s.AgentRun),
				})
			}
		}
	}
	return errs
}

func checkInputFromRefs(stages []Stage) []ValidationError {
	var errs []ValidationError
	earlierStages := make(map[string]bool)
	for i, s := range stages {
		for _, ref := range s.InputFrom {
			if !earlierStages[ref] {
				errs = append(errs, ValidationError{
					Path:    fmt.Sprintf("stages[%d].input_from", i),
					Message: fmt.Sprintf("input_from %q must reference a stage defined earlier", ref),
				})
			}
		}
		stageType := s.Type
		if stageType == "" {
			stageType = "serial"
		}
		if stageType == "parallel" {
			for j, t := range s.Tasks {
				for _, ref := range t.InputFrom {
					if !earlierStages[ref] {
						errs = append(errs, ValidationError{
							Path:    fmt.Sprintf("stages[%d].tasks[%d].input_from", i, j),
							Message: fmt.Sprintf("input_from %q must reference a stage defined earlier", ref),
						})
					}
				}
			}
		}
		earlierStages[s.Name] = true
	}
	return errs
}

func checkReachability(stages []Stage) []ValidationError {
	if len(stages) <= 1 {
		return nil
	}
	var errs []ValidationError
	reachable := make(map[string]bool)
	reachable[stages[0].Name] = true

	changed := true
	for changed {
		changed = false
		for _, s := range stages {
			if !reachable[s.Name] {
				continue
			}
			for _, r := range s.Routes {
				if r.Goto != "__done__" && r.Goto != "__escalate__" && !reachable[r.Goto] {
					reachable[r.Goto] = true
					changed = true
				}
			}
		}
	}

	for i, s := range stages {
		if i == 0 {
			continue
		}
		if !reachable[s.Name] {
			errs = append(errs, ValidationError{
				Path:    fmt.Sprintf("stages[%d]", i),
				Message: fmt.Sprintf("stage %q is unreachable (no goto from any other stage leads here)", s.Name),
			})
		}
	}
	return errs
}
