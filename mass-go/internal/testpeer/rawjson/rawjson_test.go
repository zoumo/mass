package rawjson_test

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/internal/testpeer/rawjson"
)

func TestReplacePreservesSurroundingBytes(t *testing.T) {
	original := []byte(`  { "z":9007199254740993,"response":{ "request_id":"old", "payload":{"request_id":"literal","decimal":0.123456789012345678901234567890}}, "a":[-0,true,null,"\u0061"] } `)
	document, err := rawjson.Parse(original)
	require.NoError(t, err)
	assert.Equal(t, original, document.Raw())

	replacement, err := rawjson.Parse([]byte(`"new"`))
	require.NoError(t, err)
	changed, err := document.Replace([]string{"response", "request_id"}, replacement)
	require.NoError(t, err)
	assert.True(t, bytes.Equal([]byte(`  { "z":9007199254740993,"response":{ "request_id":"new", "payload":{"request_id":"literal","decimal":0.123456789012345678901234567890}}, "a":[-0,true,null,"\u0061"] } `), changed.Raw()), "only the selected ID bytes may change")
	assert.Equal(t, original, document.Raw())

	_, err = document.Replace([]string{"response", "payload", "original"}, replacement)
	require.Error(t, err)
}

func TestEqualAppliesJSONTypesWithoutNormalizingNumbers(t *testing.T) {
	for _, test := range []struct {
		left, right string
		equal       bool
	}{
		{`{"a":1,"b":[true,"a"]}`, `{"b":[true,"\u0061"],"a":1}`, true},
		{`1`, ` 1 `, true},
		{`1`, `1.0`, false},
		{`1e3`, `1000`, false},
		{`-0`, `0`, false},
		{`"\ud83d\ude00"`, `"😀"`, true},
		{`9007199254740993`, `9007199254740992`, false},
		{`true`, `1`, false},
		{`"1"`, `1`, false},
		{`{"unknown":null}`, `{}`, false},
		{`[1,2]`, `[2,1]`, false},
	} {
		t.Run(test.left+"/"+test.right, func(t *testing.T) {
			left, err := rawjson.Parse([]byte(test.left))
			require.NoError(t, err)
			right, err := rawjson.Parse([]byte(test.right))
			require.NoError(t, err)
			assert.Equal(t, test.equal, rawjson.Equal(left, right))
		})
	}
}

func TestParseRejectsAmbiguousOrInvalidJSON(t *testing.T) {
	for _, input := range []string{
		``, `{} {}`, `{"a":1,"a":2}`, `{"x":[{"a":1,"\u0061":2}]}`,
		`{"x":NaN}`, `{"x":01}`, `{"x":Infinity}`, `{"x":}`, `[1,]`,
		`"\ud800"`, `"\udfff"`, `"\ud800\u0041"`,
	} {
		t.Run(input, func(t *testing.T) {
			_, err := rawjson.Parse([]byte(input))
			require.Error(t, err)
		})
	}
}
