package claude_test

import (
	"encoding/json"

	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
)

func (s *runtimeSuite) TestQuestionSemanticsAndOriginalInput() {
	input := `{"future_top":{"keep":7},"questions":[{"question":"Single?","header":"One","future_q":true,"options":[{"label":"A","description":"first","future_option":9}]},{"question":"Many?","multiSelect":true,"options":[{"label":"B"},{"label":"C"}]}]}`
	updated := `{"future_top":{"keep":7},"questions":[{"question":"Single?","header":"One","future_q":true,"options":[{"label":"A","description":"first","future_option":9}]},{"question":"Many?","multiSelect":true,"options":[{"label":"B"},{"label":"C"}]}],"answers":{"Single?":"free form","Many?":["B","C"]}}`
	frames := append(startup(), send(), `server control {"type":"control_request","request_id":"question","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","input":`+input+`}}`, `client control {"type":"control_response","response":{"subtype":"success","request_id":"question","response":{"behavior":"allow","updatedInput":`+updated+`}}}`)
	_, session, ctx := s.open(frames)
	e := s.nextInteraction(session, ctx)
	s.Require().Len(e.Interaction.Questions, 2)
	s.False(e.Interaction.Questions[0].Multiple)
	s.True(e.Interaction.Questions[1].Multiple)
	s.Equal("first", e.Interaction.Questions[0].Options[0].Description)
	s.Nil(e.Interaction.Questions[0].Required)
	s.Nil(e.Interaction.Questions[0].Secret)
	ref := e.Interaction.Ref
	text := "free form"
	for _, response := range []rt.Response{
		{Decision: "allow_once"},
		{Answers: map[string]rt.Answer{"unknown": {Text: &text}}},
		{Answers: map[string]rt.Answer{"Single?": {Selections: []string{"A", "A"}}}},
		{Answers: map[string]rt.Answer{"Many?": {Selections: []string{"B", "invented"}}}},
		{Answers: map[string]rt.Answer{"Many?": {Text: &text, Selections: []string{"B"}}}},
	} {
		r, err := session.Respond(ctx, ref, response)
		s.Error(err)
		s.Equal(rt.Known(false), r.HandedToSDK)
	}
	wire, err := json.Marshal(e)
	s.Require().NoError(err)
	s.NotContains(string(wire), "future_top")
	_, err = session.Respond(ctx, ref, rt.Response{Answers: map[string]rt.Answer{"Single?": {Text: &text}, "Many?": {Selections: []string{"B", "C"}}}})
	s.Require().NoError(err)
}
