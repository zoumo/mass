package replay

import "github.com/zoumo/mass/mass-go/internal/testpeer/rawjson"

type codexAdapter struct {
	requests
	threads map[id]string
}

func (a *codexAdapter) client(expected, actual rawjson.Value) (bool, error) {
	reverseAnswer := false
	if recordedID, exists := expected.Member("id"); exists {
		key, err := identity(recordedID, false)
		if err != nil {
			return false, err
		}
		actualID := member(actual, "id")
		live, err := identity(actualID, false)
		if err != nil {
			return false, err
		}
		if _, request := expected.Member("method"); request {
			if err := a.bind(key, live, actualID); err != nil {
				return false, err
			}
			actual, err = actual.Replace([]string{"id"}, recordedID)
			if err != nil {
				return false, err
			}
		} else {
			if err := a.answer(key); err != nil {
				return false, err
			}
			reverseAnswer = true
		}
	}
	return reverseAnswer, match(expected, actual)
}

func (a *codexAdapter) server(expected rawjson.Value) (rawjson.Value, error) {
	emitted := expected
	if recordedID, exists := expected.Member("id"); exists {
		key, err := identity(recordedID, false)
		if err != nil {
			return rawjson.Value{}, err
		}
		if _, request := expected.Member("method"); request {
			if err := a.reverse(key); err != nil {
				return rawjson.Value{}, err
			}
			thread := member(member(expected, "params"), "threadId")
			if thread.Kind() == rawjson.String {
				a.threads[key], _ = thread.String()
			}
		} else {
			live, err := a.response(key)
			if err != nil {
				return rawjson.Value{}, err
			}
			emitted, err = expected.Replace([]string{"id"}, live)
			if err != nil {
				return rawjson.Value{}, err
			}
		}
	}
	if stringMember(expected, "method") == "serverRequest/resolved" {
		params := member(expected, "params")
		key, err := identity(member(params, "requestId"), false)
		thread, exists := a.threads[key]
		actualThread, threadErr := member(params, "threadId").String()
		if err == nil && exists && threadErr == nil && thread == actualThread {
			delete(a.pending, key)
		}
	}
	return emitted, nil
}
