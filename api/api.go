// Package api implements the high-level API that is exposed to clients.
// Since we have many clients in many different languages, this is implemented
// language/host-indepedently, and further adapted by different packages.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/attic-labs/noms/go/hash"

	"roci.dev/replicant/api/shared"
	"roci.dev/replicant/db"
	"roci.dev/replicant/exec"
	"roci.dev/replicant/util/chk"
	jsnoms "roci.dev/replicant/util/noms/json"
)

type API struct {
	db      *db.DB
	sp      syncProgress
	syncing int32
}

type syncProgress struct {
	bytesReceived uint64
	bytesExpected uint64
}

func New(db *db.DB) *API {
	return &API{db: db}
}

func (api *API) Dispatch(name string, req []byte) ([]byte, error) {
	switch name {
	case "getRoot":
		return api.dispatchGetRoot(req)
	case "has":
		return api.dispatchHas(req)
	case "get":
		return api.dispatchGet(req)
	case "scan":
		return api.dispatchScan(req)
	case "put":
		return api.dispatchPut(req)
	case "del":
		return api.dispatchDel(req)
	case "getBundle":
		return api.dispatchGetBundle(req)
	case "putBundle":
		return api.dispatchPutBundle(req)
	case "exec":
		return api.dispatchExec(req)
	case "requestSync":
		return api.dispatchRequestSync(req)
	case "syncProgress":
		return api.dispatchSyncProgress(req)
	case "handleSync":
		return api.dispatchHandleSync(req)
	}
	chk.Fail("Unsupported rpc name: %s", name)
	return nil, nil
}

func (api *API) dispatchGetRoot(reqBytes []byte) ([]byte, error) {
	var req shared.GetRootRequest
	err := json.Unmarshal(reqBytes, &req)
	if err != nil {
		return nil, err
	}

	res := shared.GetRootResponse{
		Root: jsnoms.Hash{
			Hash: api.db.Hash(),
		},
	}
	return mustMarshal(res), nil
}

func (api *API) dispatchHas(reqBytes []byte) ([]byte, error) {
	var req shared.HasRequest
	err := json.Unmarshal(reqBytes, &req)
	if err != nil {
		return nil, err
	}
	ok, err := api.db.Has(req.ID)
	if err != nil {
		return nil, err
	}
	res := shared.HasResponse{
		Has: ok,
	}
	return mustMarshal(res), nil
}

func (api *API) dispatchGet(reqBytes []byte) ([]byte, error) {
	var req shared.GetRequest
	err := json.Unmarshal(reqBytes, &req)
	if err != nil {
		return nil, err
	}
	v, err := api.db.Get(req.ID)
	if err != nil {
		return nil, err
	}
	res := shared.GetResponse{}
	if v == nil {
		res.Has = false
	} else {
		res.Has = true
		res.Value = jsnoms.New(api.db.Noms(), v)
	}
	return mustMarshal(res), nil
}

func (api *API) dispatchScan(reqBytes []byte) ([]byte, error) {
	var req shared.ScanRequest
	err := json.Unmarshal(reqBytes, &req)
	if err != nil {
		return nil, err
	}
	items, err := api.db.Scan(exec.ScanOptions(req))
	if err != nil {
		return nil, err
	}
	return mustMarshal(items), nil
}

func (api *API) dispatchPut(reqBytes []byte) ([]byte, error) {
	req := shared.PutRequest{
		Value: jsnoms.Make(api.db.Noms(), nil),
	}
	err := json.Unmarshal(reqBytes, &req)
	if err != nil {
		return nil, err
	}
	if req.Value.Value == nil {
		return nil, errors.New("value field is required")
	}
	err = api.db.Put(req.ID, req.Value.Value)
	if err != nil {
		return nil, err
	}
	res := shared.PutResponse{
		Root: jsnoms.Hash{
			Hash: api.db.Hash(),
		},
	}
	return mustMarshal(res), nil
}

func (api *API) dispatchDel(reqBytes []byte) ([]byte, error) {
	req := shared.DelRequest{}
	err := json.Unmarshal(reqBytes, &req)
	if err != nil {
		return nil, err
	}
	ok, err := api.db.Del(req.ID)
	if err != nil {
		return nil, err
	}
	res := shared.DelResponse{
		Ok: ok,
		Root: jsnoms.Hash{
			Hash: api.db.Hash(),
		},
	}
	return mustMarshal(res), nil
}

func (api *API) dispatchGetBundle(reqBytes []byte) ([]byte, error) {
	var req shared.GetBundleRequest
	err := json.Unmarshal(reqBytes, &req)
	if err != nil {
		return nil, err
	}
	return mustMarshal(shared.GetBundleResponse{
		Code: string(api.db.Bundle()),
	}), nil
}

func (api *API) dispatchPutBundle(reqBytes []byte) ([]byte, error) {
	var req shared.PutBundleRequest
	err := json.Unmarshal(reqBytes, &req)
	if err != nil {
		return nil, err
	}
	err = api.db.PutBundle([]byte(req.Code))
	if err != nil {
		return nil, errors.New(err.Error())
	}
	res := shared.PutBundleResponse{
		Root: jsnoms.Hash{
			Hash: api.db.Hash(),
		},
	}
	return mustMarshal(res), nil
}

func (api *API) dispatchExec(reqBytes []byte) ([]byte, error) {
	req := shared.ExecRequest{
		Args: jsnoms.List{
			Value: jsnoms.Make(api.db.Noms(), nil),
		},
	}
	err := json.Unmarshal(reqBytes, &req)
	if err != nil {
		return nil, err
	}
	output, err := api.db.Exec(req.Name, req.Args.List())
	if err != nil {
		return nil, err
	}
	res := shared.ExecResponse{
		Root: jsnoms.Hash{
			Hash: api.db.Hash(),
		},
	}
	if output != nil {
		res.Result = jsnoms.New(api.db.Noms(), output)
	}
	return mustMarshal(res), nil
}

func (api *API) dispatchRequestSync(reqBytes []byte) ([]byte, error) {
	var req shared.SyncRequest
	err := json.Unmarshal(reqBytes, &req)
	if err != nil {
		return nil, err
	}

	if !atomic.CompareAndSwapInt32(&api.syncing, 0, 1) {
		return nil, errors.New("There is already a sync in progress")
	}

	defer chk.True(atomic.CompareAndSwapInt32(&api.syncing, 1, 0), "UNEXPECTED STATE: Overlapping syncs somehow!")

	req.Remote.Options.Authorization = req.Auth

	res := shared.SyncResponse{}
	err = api.db.RequestSync(req.Remote.Spec, func(received, expected uint64) {
		api.sp = syncProgress{
			bytesReceived: received,
			bytesExpected: expected,
		}
	})
	if _, ok := err.(db.SyncAuthError); ok {
		res.Error = &shared.SyncResponseError{
			BadAuth: err.Error(),
		}
		err = nil
	}
	if err != nil {
		return nil, err
	}
	if res.Error == nil {
		res.Root = jsnoms.Hash{
			Hash: api.db.Hash(),
		}
	}
	return mustMarshal(res), nil
}

func (api *API) dispatchSyncProgress(reqBytes []byte) ([]byte, error) {
	var req shared.SyncProgressRequest
	err := json.Unmarshal(reqBytes, &req)
	if err != nil {
		return nil, err
	}
	res := shared.SyncProgressResponse{
		BytesReceived: api.sp.bytesReceived,
		BytesExpected: api.sp.bytesExpected,
	}
	return mustMarshal(res), nil
}

func (api *API) dispatchHandleSync(reqBytes []byte) ([]byte, error) {
	var req shared.HandleSyncRequest
	err := json.Unmarshal(reqBytes, &req)
	if err != nil {
		return nil, err
	}
	var h hash.Hash
	if req.Basis != "" {
		var ok bool
		h, ok = hash.MaybeParse(req.Basis)
		if !ok {
			return nil, fmt.Errorf("Invalid basis hash")
		}
	}
	r, err := api.db.HandleSync(h)
	if err != nil {
		return nil, err
	}
	res := shared.HandleSyncResponse{
		CommitID: api.db.Head().Original.Hash().String(),
		Patch:    r,
		// TODO: This is a bummer. The checksum doesn't include the code.
		// Can't just easily add the code in because the keys in our data here aren't
		// namespaced in any way. What we really want to do is move the code into the
		// main map and namespace all the data so that we can just use the Noms checksum.
		// Alternately, we could move to lthash which wouldn't require us to materialize
		// the map in order to update the checksum.
		NomsChecksum: api.db.Head().Data(api.db.Noms()).Hash().String(),
	}
	return mustMarshal(res), nil
}

func mustMarshal(thing interface{}) []byte {
	data, err := json.Marshal(thing)
	chk.NoError(err)
	return data
}
