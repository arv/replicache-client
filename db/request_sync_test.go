package db

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/attic-labs/noms/go/marshal"
	"github.com/attic-labs/noms/go/spec"
	"github.com/attic-labs/noms/go/types"
	"github.com/stretchr/testify/assert"
	"roci.dev/replicant/api/shared"
)

func TestRequestSync(t *testing.T) {
	assert := assert.New(t)

	tc := []struct {
		label                    string
		initialState             map[string]string
		initialBasis             string
		reqError                 bool
		respCode                 int
		respBody                 string
		expectedError            string
		expectedErrorIsAuthError bool
		expectedCode             string
		expectedData             map[string]string
		expectedBasis            string
	}{
		{
			"ok-nop",
			map[string]string{},
			"",
			false,
			http.StatusOK,
			`{"patch":[],"commitID":"11111111111111111111111111111111","nomsChecksum":"t13tdcmq2d3pkpt9avk4p4nbt1oagaa3"}`,
			"",
			false,
			"",
			map[string]string{},
			"11111111111111111111111111111111",
		},
		{
			"ok-no-basis",
			map[string]string{},
			"",
			false,
			http.StatusOK,
			`{"patch":[{"op":"add","path":"/u/foo","value":"bar"}],"commitID":"11111111111111111111111111111111","nomsChecksum":"am8lvhrbscqkngg75jaiubirapurghv9"}`,
			"",
			false,
			"",
			map[string]string{"foo": "bar"},
			"11111111111111111111111111111111",
		},
		{
			"ok-with-basis",
			map[string]string{},
			"11111111111111111111111111111111",
			false,
			http.StatusOK,
			`{"patch":[{"op":"add","path":"/u/foo","value":"bar"}],"commitID":"22222222222222222222222222222222","nomsChecksum":"am8lvhrbscqkngg75jaiubirapurghv9"}`,
			"",
			false,
			"",
			map[string]string{"foo": "bar"},
			"22222222222222222222222222222222",
		},
		{
			"ok-change-code",
			map[string]string{},
			"11111111111111111111111111111111",
			false,
			http.StatusOK,
			`{"patch":[{"op":"add","path":"/u/foo","value":"bar"},{"op":"replace","path":"/s/code","value":"function foo(){}"}],"commitID":"22222222222222222222222222222222","nomsChecksum":"am8lvhrbscqkngg75jaiubirapurghv9"}`,
			"",
			false,
			"function foo(){}",
			map[string]string{"foo": "bar"},
			"22222222222222222222222222222222",
		},
		{
			"network-error",
			map[string]string{"foo": "bar"},
			"11111111111111111111111111111111",
			true,
			http.StatusOK,
			``,
			`Post http://127.0.0.1:\d+/handleSync: dial tcp 127.0.0.1:\d+: connect: connection refused`,
			false,
			"",
			map[string]string{"foo": "bar"},
			"11111111111111111111111111111111",
		},
		{
			"http-error",
			map[string]string{"foo": "bar"},
			"11111111111111111111111111111111",
			false,
			http.StatusBadRequest,
			"You have made an invalid request",
			"400 Bad Request: You have made an invalid request",
			false,
			"",
			map[string]string{"foo": "bar"},
			"11111111111111111111111111111111",
		},
		{
			"invalid-response",
			map[string]string{"foo": "bar"},
			"11111111111111111111111111111111",
			false,
			http.StatusOK,
			"this isn't valid json!",
			`Response from http://127.0.0.1:\d+/handleSync is not valid JSON: invalid character 'h' in literal true \(expecting 'r'\)`,
			false,
			"",
			map[string]string{"foo": "bar"},
			"11111111111111111111111111111111",
		},
		{
			"empty-response",
			map[string]string{"foo": "bar"},
			"11111111111111111111111111111111",
			false,
			http.StatusOK,
			"",
			`Response from http://127.0.0.1:\d+/handleSync is not valid JSON: EOF`,
			false,
			"",
			map[string]string{"foo": "bar"},
			"11111111111111111111111111111111",
		},
		{
			"nuke-first-patch",
			map[string]string{"foo": "bar"},
			"11111111111111111111111111111111",
			false,
			http.StatusOK,
			`{"patch":[{"op":"remove","path":"/"},{"op":"add","path":"/u/foo","value":"baz"}],"commitID":"22222222222222222222222222222222","nomsChecksum":"e4ankqlqffbmkl8bek60auevqti3gbgi"}`,
			"",
			false,
			"",
			map[string]string{"foo": "baz"},
			"22222222222222222222222222222222",
		},
		{
			"invalid-patch-nuke-late-patch",
			map[string]string{"foo": "bar"},
			"11111111111111111111111111111111",
			false,
			http.StatusOK,
			`{"patch":[{"op":"add","path":"/u/foo","value":"baz"},{"op":"remove","path":"/"}],"commitID":"22222222222222222222222222222222","nomsChecksum":"am8lvhrbscqkngg75jaiubirapurghv9"}`,
			"Unsupported JSON Patch operation: remove with path: /",
			false,
			"",
			map[string]string{"foo": "bar"},
			"11111111111111111111111111111111",
		},
		{
			"invalid-patch-bad-code",
			map[string]string{"foo": "bar"},
			"11111111111111111111111111111111",
			false,
			http.StatusOK,
			`{"patch":[{"op":"add","path":"/s/code","value":42}],"commitID":"22222222222222222222222222222222","nomsChecksum":"am8lvhrbscqkngg75jaiubirapurghv9"}`,
			"Cannot unmarshal /s/code: json: cannot unmarshal number into Go value of type string",
			false,
			"",
			map[string]string{"foo": "bar"},
			"11111111111111111111111111111111",
		},
		{
			"invalid-patch-bad-op",
			map[string]string{"foo": "bar"},
			"11111111111111111111111111111111",
			false,
			http.StatusOK,
			`{"patch":[{"op":"add","path":"/u/foo"}],"commitID":"22222222222222222222222222222222","nomsChecksum":"am8lvhrbscqkngg75jaiubirapurghv9"}`,
			"Cannot unmarshal /u/foo: EOF",
			false,
			"",
			map[string]string{"foo": "bar"},
			"11111111111111111111111111111111",
		},
		{
			"invalid-patch-bad-op",
			map[string]string{"foo": "bar"},
			"11111111111111111111111111111111",
			false,
			http.StatusOK,
			`{"patch":[{"op":"monkey"}],"commitID":"22222222222222222222222222222222","nomsChecksum":"am8lvhrbscqkngg75jaiubirapurghv9"}`,
			"Unsupported JSON Patch operation: monkey with path: ",
			false,
			"",
			map[string]string{"foo": "bar"},
			"11111111111111111111111111111111",
		},
		{
			"checksum-mismatch",
			map[string]string{},
			"11111111111111111111111111111111",
			false,
			http.StatusOK,
			`{"patch":[{"op":"add","path":"/u/foo","value":"bar"}],"commitID":"22222222222222222222222222222222","nomsChecksum":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`,
			"Checksum mismatch!",
			false,
			"",
			map[string]string{},
			"11111111111111111111111111111111",
		},
		{
			"auth-error",
			map[string]string{},
			"",
			false,
			http.StatusForbidden,
			`Bad auth token`,
			"Forbidden: Bad auth token",
			true,
			"",
			map[string]string{},
			"",
		},
	}

	for _, t := range tc {
		db, dir := LoadTempDB(assert)
		fmt.Println("dir", dir)
		g := makeGenesis(db.noms, t.initialBasis)
		if t.initialState != nil {
			ed := g.Data(db.noms).Edit()
			for k, v := range t.initialState {
				ed.Set(types.String(k), types.String(v))
			}
			g.Value.Data = db.noms.WriteValue(ed.Map())
		}
		db.noms.SetHead(db.noms.GetDataset(LOCAL_DATASET), db.noms.WriteValue(marshal.MustMarshal(db.noms, g)))
		err := db.Reload()
		assert.NoError(err, t.label)

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var reqBody shared.HandleSyncRequest
			err := json.NewDecoder(r.Body).Decode(&reqBody)
			assert.NoError(err, t.label)
			assert.Equal(t.initialBasis, reqBody.Basis, t.label)
			w.WriteHeader(t.respCode)
			w.Write([]byte(t.respBody))
		}))

		if t.reqError {
			server.Close()
		}

		sp, err := spec.ForDatabase(server.URL)
		assert.NoError(err, t.label)

		err = db.RequestSync(sp)
		if t.expectedError == "" {
			assert.NoError(err, t.label)
		} else {
			assert.Regexp(t.expectedError, err.Error(), t.label)
			_, ok := err.(SyncAuthError)
			assert.Equal(t.expectedErrorIsAuthError, ok, t.label)
		}

		ee := types.NewMap(db.noms).Edit()
		for k, v := range t.expectedData {
			ee.Set(types.String(k), types.String(v))
		}
		expected := ee.Map()
		assert.True(expected.Equals(db.head.Data(db.noms)), t.label)

		b, err := ioutil.ReadAll(db.head.Bundle(db.noms).Reader())
		assert.NoError(err, t.label)
		assert.Equal(t.expectedCode, string(b), t.label)

		assert.Equal(t.expectedBasis, db.head.Meta.Genesis.ServerCommitID, t.label)
	}
}
