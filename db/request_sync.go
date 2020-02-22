package db

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"strconv"
	"strings"

	servetypes "roci.dev/diff-server/serve/types"
	"roci.dev/diff-server/util/chk"
	"roci.dev/diff-server/util/countingreader"
	"roci.dev/diff-server/util/noms/jsonpatch"

	"github.com/attic-labs/noms/go/marshal"
	"github.com/attic-labs/noms/go/spec"
	"github.com/attic-labs/noms/go/types"
	"github.com/attic-labs/noms/go/util/verbose"
)

type SyncAuthError struct {
	error
}

type Progress func(bytesReceived, bytesExpected uint64)

// RequestSync kicks off the new patch-based sync protocol from the client side.
func (db *DB) RequestSync(remote spec.Spec, progress Progress) error {
	url := fmt.Sprintf("%s/handleSync", remote.String())
	reqBody, err := json.Marshal(servetypes.HandleSyncRequest{
		Basis: db.head.Meta.Genesis.ServerCommitID,
	})
	verbose.Log("Syncing: %s from basis %s", url, db.head.Meta.Genesis.ServerCommitID)
	chk.NoError(err)

	req, err := http.NewRequest("POST", url, bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	req.Header.Add("Authorization", remote.Options.Authorization)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}

	if resp.StatusCode != http.StatusOK {
		body, err := ioutil.ReadAll(resp.Body)
		var s string
		if err == nil {
			s = string(body)
		} else {
			s = err.Error()
		}
		err = fmt.Errorf("%s: %s", resp.Status, s)
		if resp.StatusCode == http.StatusForbidden {
			return SyncAuthError{
				err,
			}
		} else {
			return err
		}
	}

	getExpectedLength := func() (r int64, err error) {
		var s = resp.Header.Get("Entity-length")
		if s != "" {
			r, err = strconv.ParseInt(s, 10, 64)
			if err != nil {
				return 0, fmt.Errorf("Non-integral value for Entity-length header: %s", s)
			}
			return r, nil
		}
		if resp.ContentLength >= 0 {
			return resp.ContentLength, nil
		}
		return 0, nil
	}

	var respBody servetypes.HandleSyncResponse
	var r io.Reader = resp.Body
	if progress != nil {
		cr := &countingreader.Reader{
			R: resp.Body,
		}
		expected, err := getExpectedLength()
		if err != nil {
			return err
		}
		cr.Callback = func() {
			rec := cr.Count
			exp := uint64(expected)
			if exp == 0 {
				exp = rec
			} else if rec > exp {
				rec = exp
			}
			progress(rec, exp)
		}
		r = cr
	}
	err = json.NewDecoder(r).Decode(&respBody)
	if err != nil {
		return fmt.Errorf("Response from %s is not valid JSON: %s", url, err.Error())
	}

	var patch = respBody.Patch
	head := makeGenesis(db.noms, respBody.CommitID)
	if len(patch) > 0 && patch[0].Op == jsonpatch.OpRemove && patch[0].Path == "/" {
		patch = patch[1:]
	} else {
		head.Value = db.head.Value
	}

	var ed *types.MapEditor
	for _, op := range patch {
		switch {
		case strings.HasPrefix(op.Path, "/u"):
			if ed == nil {
				ed = db.head.Data(db.noms).Edit()
			}
			origPath := op.Path
			op.Path = op.Path[2:]
			err = jsonpatch.ApplyOne(db.noms, ed, op)
			if err != nil {
				return fmt.Errorf("Cannot unmarshal %s: %s", origPath, err.Error())
			}
		default:
			return fmt.Errorf("Unsupported JSON Patch operation: %s with path: %s", op.Op, op.Path)
		}
	}

	if ed != nil {
		head.Value.Data = db.noms.WriteValue(ed.Map())
	}
	if head.Value.Data.TargetHash().String() != respBody.NomsChecksum {
		return fmt.Errorf("Checksum mismatch! Expected %s, got %s", respBody.NomsChecksum, head.Value.Data.TargetHash())
	}
	db.noms.SetHead(db.noms.GetDataset(LOCAL_DATASET), db.noms.WriteValue(marshal.MustMarshal(db.noms, head)))
	return db.init()
}
