package pipeline

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func validPipeline() *Pipeline {
	return &Pipeline{
		Name: "test",
		AgentRuns: map[string]AgentRun{
			"agent1": {Agent: "claude"},
			"agent2": {Agent: "codex"},
		},
		Stages: []Stage{
			{
				Name: "step1", AgentRun: "agent1", Description: "do something",
				Routes: []Route{{When: "success", Goto: "step2"}, {When: "failed", Goto: "__escalate__"}},
			},
			{
				Name: "step2", AgentRun: "agent2", Description: "do more",
				InputFrom: []string{"step1"},
				Routes:    []Route{{When: "success", Goto: "__done__"}, {When: "failed", Goto: "step1"}},
			},
		},
	}
}

func TestValidateSemantic_Valid(t *testing.T) {
	errs := ValidateSemantic(validPipeline())
	assert.Empty(t, errs)
}

func TestCheckStageNameUniqueness(t *testing.T) {
	stages := []Stage{
		{Name: "a"}, {Name: "b"}, {Name: "a"},
	}
	errs := checkStageNameUniqueness(stages)
	assert.Len(t, errs, 1)
	assert.Contains(t, errs[0].Message, "duplicate stage name")
}

func TestCheckReservedStageNames(t *testing.T) {
	stages := []Stage{
		{Name: "__done__"}, {Name: "ok"}, {Name: "__escalate__"},
	}
	errs := checkReservedStageNames(stages)
	assert.Len(t, errs, 2)
}

func TestCheckGotoTargets(t *testing.T) {
	stages := []Stage{
		{Name: "a", Routes: []Route{{When: "success", Goto: "nonexistent"}}},
	}
	errs := checkGotoTargets(stages)
	assert.Len(t, errs, 1)
	assert.Contains(t, errs[0].Message, "nonexistent")
}

func TestCheckGotoTargets_SpecialTargets(t *testing.T) {
	stages := []Stage{
		{Name: "a", Routes: []Route{
			{When: "success", Goto: "__done__"},
			{When: "failed", Goto: "__escalate__"},
		}},
	}
	errs := checkGotoTargets(stages)
	assert.Empty(t, errs)
}

func TestCheckAgentRunRefs(t *testing.T) {
	p := &Pipeline{
		AgentRuns: map[string]AgentRun{"real": {Agent: "claude"}},
		Stages: []Stage{
			{Name: "s1", AgentRun: "missing"},
		},
	}
	errs := checkAgentRunRefs(p)
	assert.Len(t, errs, 1)
	assert.Contains(t, errs[0].Message, "missing")
}

func TestCheckAgentRunRefs_Parallel(t *testing.T) {
	p := &Pipeline{
		AgentRuns: map[string]AgentRun{"real": {Agent: "claude"}},
		Stages: []Stage{
			{
				Name: "s1", Type: "parallel",
				Tasks: []Task{
					{AgentRun: "real", Description: "ok"},
					{AgentRun: "ghost", Description: "bad"},
				},
			},
		},
	}
	errs := checkAgentRunRefs(p)
	assert.Len(t, errs, 1)
	assert.Contains(t, errs[0].Message, "ghost")
}

func TestCheckInputFromRefs(t *testing.T) {
	stages := []Stage{
		{Name: "a"},
		{Name: "b", InputFrom: []string{"a", "c"}},
	}
	errs := checkInputFromRefs(stages)
	assert.Len(t, errs, 1)
	assert.Contains(t, errs[0].Message, "c")
}

func TestCheckInputFromRefs_ForwardRef(t *testing.T) {
	stages := []Stage{
		{Name: "a", InputFrom: []string{"b"}},
		{Name: "b"},
	}
	errs := checkInputFromRefs(stages)
	assert.Len(t, errs, 1)
	assert.Contains(t, errs[0].Message, "b")
}

func TestCheckReachability(t *testing.T) {
	stages := []Stage{
		{Name: "a", Routes: []Route{{When: "success", Goto: "__done__"}}},
		{Name: "orphan", Routes: []Route{{When: "success", Goto: "__done__"}}},
	}
	errs := checkReachability(stages)
	assert.Len(t, errs, 1)
	assert.Contains(t, errs[0].Message, "orphan")
	assert.Contains(t, errs[0].Message, "unreachable")
}

func TestCheckReachability_SingleStage(t *testing.T) {
	stages := []Stage{
		{Name: "only", Routes: []Route{{When: "success", Goto: "__done__"}}},
	}
	errs := checkReachability(stages)
	assert.Empty(t, errs)
}

func TestCheckReachability_TransitiveOK(t *testing.T) {
	stages := []Stage{
		{Name: "a", Routes: []Route{{When: "success", Goto: "b"}}},
		{Name: "b", Routes: []Route{{When: "success", Goto: "c"}}},
		{Name: "c", Routes: []Route{{When: "success", Goto: "__done__"}}},
	}
	errs := checkReachability(stages)
	assert.Empty(t, errs)
}
