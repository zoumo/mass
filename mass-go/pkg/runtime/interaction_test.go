package runtime_test

import (
	"encoding/json"

	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
)

func (s *eventSuite) TestQuestionResponseFormsRemainDistinct() {
	text := ""
	required := true
	free := true
	secret := true
	spec := rt.Interaction{Ref: "scoped", Kind: "question", State: "requested", Questions: []rt.Question{{ID: "q", Prompt: "Value?", Multiple: true, Required: &required, FreeText: &free, Secret: &secret, Options: []rt.QuestionOption{{Label: "A", Description: "first"}}}}}
	for _, tc := range []struct {
		name   string
		answer rt.Answer
		valid  bool
	}{
		{"empty scalar", rt.Answer{Text: &text}, true},
		{"empty multiple", rt.Answer{Selections: []string{}}, true},
		{"selection", rt.Answer{Selections: []string{"A"}}, true},
		{"missing form", rt.Answer{}, false},
		{"two forms", rt.Answer{Text: &text, Selections: []string{}}, false},
		{"unknown label", rt.Answer{Selections: []string{"invented"}}, false},
	} {
		s.Run(tc.name, func() {
			response := rt.Response{Answers: map[string]rt.Answer{"q": tc.answer}}
			wire, err := json.Marshal(response)
			s.Require().NoError(err)
			var replay rt.Response
			s.Require().NoError(json.Unmarshal(wire, &replay))
			s.Equal(response, replay)
			err = spec.Validate(replay)
			if tc.valid {
				s.NoError(err)
			} else {
				s.Error(err)
			}
		})
	}
	s.Error(spec.Validate(rt.Response{Answers: map[string]rt.Answer{}}), "required answer cannot be omitted")
	event := eventWithMessage()
	event.Kind = rt.InteractionRequested
	event.Message = nil
	event.Interaction = &spec
	wire, err := json.Marshal(event)
	s.Require().NoError(err)
	var replay rt.Event
	s.Require().NoError(json.Unmarshal(wire, &replay))
	s.Equal(event, replay)
}
