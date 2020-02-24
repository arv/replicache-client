package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	gtime "time"

	"github.com/attic-labs/noms/go/spec"
	"github.com/stretchr/testify/assert"

	jsnoms "roci.dev/diff-server/util/noms/json"
	"roci.dev/diff-server/util/time"
	"roci.dev/replicache-client/api/shared"
	"roci.dev/replicache-client/db"
)

func TestBasics(t *testing.T) {
	assert := assert.New(t)
	local, dir := db.LoadTempDB(assert)
	fmt.Println(dir)
	api := New(local)

	defer time.SetFake()()

	const invalidRequest = ""
	const invalidRequestError = "unexpected end of JSON input"
	code, err := json.Marshal(`function add(key, d) { var v = db.get(key) || 0; v += d; db.put(key, v); return v; }
	function log(key, val) { var v = db.get(key) || []; v.push(val); db.put(key, v); }`)
	assert.NoError(err)

	tc := []struct {
		rpc              string
		req              string
		expectedResponse string
		expectedError    string
	}{
		// invalid json for all cases
		// valid json + success case for all cases
		// valid json + failure case for all cases
		// attempt to write non-json with put()
		// attempt to read non-json with get()

		// getRoot on empty db
		{"getRoot", `{}`, `{"root":"uosmsi0mbbd1qgf2m0rgfkcrhf32c7om"}`, ""},

		// put
		{"put", invalidRequest, ``, invalidRequestError},
		{"getRoot", `{}`, `{"root":"uosmsi0mbbd1qgf2m0rgfkcrhf32c7om"}`, ""}, // getRoot when db didn't change
		{"put", `{"id": "foo"}`, ``, "value field is required"},
		{"put", `{"id": "foo", "value": null}`, ``, "value field is required"},
		{"put", `{"id": "foo", "value": "bar"}`, `{"root":"nti2kt1b288sfhdmqkgnjrog52a7m8ob"}`, ""},
		{"getRoot", `{}`, `{"root":"nti2kt1b288sfhdmqkgnjrog52a7m8ob"}`, ""}, // getRoot when db did change

		// has
		{"has", invalidRequest, ``, invalidRequestError},
		{"has", `{"id": "foo"}`, `{"has":true}`, ""},

		// get
		{"get", invalidRequest, ``, invalidRequestError},
		{"get", `{"id": "foo"}`, `{"has":true,"value":"bar"}`, ""},

		// putBundle
		{"putBundle", invalidRequest, ``, invalidRequestError},
		{"putBundle", fmt.Sprintf(`{"code": %s}`, string(code)), `{"root":"nti2kt1b288sfhdmqkgnjrog52a7m8ob"}`, ""},

		// getBundle
		{"getBundle", invalidRequest, ``, invalidRequestError},
		{"getBundle", `{}`, fmt.Sprintf(`{"code":%s}`, string(code)), ""},

		// exec
		{"exec", invalidRequest, ``, invalidRequestError},
		{"exec", `{"name": "add", "args": ["bar", 2]}`, `{"result":2,"root":"01aj0nvumggim5hkm0atuf0s73p9l51e"}`, ""},
		{"get", `{"id": "bar"}`, `{"has":true,"value":2}`, ""},

		// scan
		{"put", `{"id": "foopa", "value": "doopa"}`, `{"root":"dsvkq4dji7v7kbj70b5tml1go53k516q"}`, ""},
		{"scan", `{"prefix": "foo"}`, `[{"id":"foo","value":"bar"},{"id":"foopa","value":"doopa"}]`, ""},
		{"scan", `{"start": {"id": {"value": "foo"}}}`, `[{"id":"foo","value":"bar"},{"id":"foopa","value":"doopa"}]`, ""},
		{"scan", `{"start": {"id": {"value": "foo", "exclusive": true}}}`, `[{"id":"foopa","value":"doopa"}]`, ""},

		// TODO: other scan operators
	}

	for _, t := range tc {
		res, err := api.Dispatch(t.rpc, []byte(t.req))
		if t.expectedError != "" {
			assert.Nil(res, "test case %s: %s", t.rpc, t.req, "test case %s: %s", t.rpc, t.req)
			assert.EqualError(err, t.expectedError, "test case %s: %s", t.rpc, t.req)
		} else {
			assert.Equal(t.expectedResponse, string(res), "test case %s: %s", t.rpc, t.req)
			assert.NoError(err, "test case %s: %s", t.rpc, t.req, "test case %s: %s", t.rpc, t.req)
		}
	}
}

func TestProgress(t *testing.T) {
	twoChunks := [][]byte{[]byte(`"foo`), []byte(`bar"`)}
	assert := assert.New(t)
	db, dir := db.LoadTempDB(assert)
	fmt.Println("dir", dir)
	api := New(db)

	getProgress := func() (received, expected uint64) {
		buf, err := api.Dispatch("syncProgress", mustMarshal(shared.SyncProgressRequest{}))
		assert.NoError(err)
		var resp shared.SyncProgressResponse
		err = json.Unmarshal(buf, &resp)
		assert.NoError(err)
		return resp.BytesReceived, resp.BytesExpected
	}

	totalLength := uint64(len(twoChunks[0]) + len(twoChunks[1]))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-length", fmt.Sprintf("%d", totalLength))
		seen := uint64(0)
		rec, exp := getProgress()
		assert.Equal(uint64(0), rec)
		assert.Equal(uint64(0), exp)
		for _, c := range twoChunks {
			seen += uint64(len(c))
			_, err := w.Write(c)
			assert.NoError(err)
			w.(http.Flusher).Flush()
			gtime.Sleep(100 * gtime.Millisecond)
			rec, exp := getProgress()
			assert.Equal(seen, rec)
			assert.Equal(totalLength, exp)
		}
	}))

	sp, err := spec.ForDatabase(server.URL)
	assert.NoError(err)
	req := shared.SyncRequest{
		Remote:  jsnoms.Spec{sp},
		Shallow: true,
	}

	_, err = api.Dispatch("requestSync", mustMarshal(req))
	assert.Regexp(`Response from [^ ]+ is not valid JSON: json: cannot unmarshal string into Go value of type types.HandleSyncResponse`, err.Error())
}
