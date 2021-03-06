// Package db implements the core database abstraction of Replicache.
package db

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/attic-labs/noms/go/d"
	"github.com/attic-labs/noms/go/datas"
	"github.com/attic-labs/noms/go/hash"
	"github.com/attic-labs/noms/go/marshal"
	"github.com/attic-labs/noms/go/spec"
	"github.com/attic-labs/noms/go/types"

	"roci.dev/diff-server/kv"
	nomsjson "roci.dev/diff-server/util/noms/json"
	"roci.dev/diff-server/util/time"
)

const (
	LOCAL_DATASET  = "local"
	REMOTE_DATASET = "remote"
)

type DB struct {
	noms     datas.Database
	head     Commit
	clientID string
	mu       sync.Mutex
}

func Load(sp spec.Spec) (*DB, error) {
	if !sp.Path.IsEmpty() {
		return nil, errors.New("Invalid spec - must not specify a path")
	}

	var noms datas.Database
	err := d.Try(func() {
		noms = sp.GetDatabase()
	})
	if err != nil {
		err = err.(d.WrappedError).Cause()
		return nil, err
	}
	return New(noms)
}

func New(noms datas.Database) (*DB, error) {
	r := DB{
		noms: noms,
	}
	defer r.lock()()
	err := r.init()
	if err != nil {
		return nil, err
	}

	return &r, nil
}

func (db *DB) init() error {
	var err error

	cid := db.clientID
	if cid == "" {
		cid, err = initClientID(db.noms)
		log.Printf("ClientID: %s", cid)
	}
	if err != nil {
		return err
	}

	ds := db.noms.GetDataset(LOCAL_DATASET)
	if !ds.HasHead() {
		m := kv.NewMap(db.noms)
		genesis := makeGenesis(db.noms, "", db.noms.WriteValue(m.NomsMap()), m.NomsChecksum(), 0 /*lastMutationID*/)
		genRef := db.noms.WriteValue(genesis.Original)
		_, err := db.noms.FastForward(ds, genRef)
		if err != nil {
			return err
		}
		db.clientID = cid
		db.head = genesis
		return nil
	}

	headType := types.TypeOf(ds.Head())
	if !types.IsSubtype(schema, headType) {
		return fmt.Errorf("Cannot load database. Specified head has non-Replicache data of type: %s", headType.Describe())
	}

	var head Commit
	err = marshal.Unmarshal(ds.Head(), &head)
	if err != nil {
		return err
	}

	db.clientID = cid
	db.head = head
	return nil
}

func (db *DB) Noms() types.ValueReadWriter {
	return db.noms
}

func (db *DB) Head() Commit {
	return db.head
}

func (db *DB) RemoteHead() (c Commit, err error) {
	ds := db.noms.GetDataset(REMOTE_DATASET)
	if !ds.HasHead() {
		// TODO: maybe setup the remote head at startup too.
		m := kv.NewMap(db.noms)
		return makeGenesis(db.noms, "", db.noms.WriteValue(m.NomsMap()), m.NomsChecksum(), 0), nil

	}
	err = marshal.Unmarshal(ds.Head(), &c)
	return
}

func (db *DB) Hash() hash.Hash {
	return db.head.Original.Hash()
}

func (db *DB) Has(id string) (bool, error) {
	return db.head.Data(db.noms).Has(types.String(id)), nil
}

func (db *DB) Get(id string) ([]byte, error) {
	value := db.head.Data(db.noms).Get(types.String(id))
	if value == nil {
		return nil, nil
	}
	var b bytes.Buffer
	err := nomsjson.ToJSON(value, &b)
	return b.Bytes(), err
}

func (db *DB) Put(path string, JSON []byte) error {
	canonicalJSON, err := nomsjson.Canonicalize(JSON)
	if err != nil {
		return fmt.Errorf("could not Put '%s'='%s': %w", path, JSON, err)
	}
	value, err := nomsjson.FromJSON(bytes.NewReader(canonicalJSON), db.Noms())
	if err != nil {
		return fmt.Errorf("could not Put '%s'='%s': %w", path, JSON, err)
	}

	defer db.lock()()
	_, err = db.execInternal(".putValue", types.NewList(db.Noms(), types.String(path), value))
	return err
}

func (db *DB) Del(path string) (ok bool, err error) {
	defer db.lock()()
	v, err := db.execInternal(".delValue", types.NewList(db.Noms(), types.String(path)))
	return bool(v.(types.Bool)), err
}

func (db *DB) Reload() error {
	defer db.lock()()
	db.noms.Rebase()
	return db.init()
}

func (db *DB) execInternal(function string, args types.List) (types.Value, error) {
	basis := types.NewRef(db.head.Original)
	newData, newDataChecksum, output, isWrite, err := db.execImpl(basis, function, args)
	if err != nil {
		return nil, err
	}

	// Do not add commits for read-only transactions.
	if !isWrite {
		return output, nil
	}

	commit := makeTx(db.noms, basis, time.DateTime(), function, args, newData, newDataChecksum)
	commitRef := db.noms.WriteValue(commit.Original)

	// FastForward not strictly needed here because we should have already ensured that we were
	// fast-forwarding outside of Noms, but it's a nice sanity check.
	_, err = db.noms.FastForward(db.noms.GetDataset(LOCAL_DATASET), commitRef)
	if err != nil {
		return nil, err
	}
	db.head = commit
	return output, nil
}

// TODO: add date and random source to this so that sync can set it up correctly when replaying.
func (db *DB) execImpl(basis types.Ref, function string, args types.List) (newDataRef types.Ref, newDataChecksum types.String, output types.Value, isWrite bool, err error) {
	var basisCommit Commit
	err = marshal.Unmarshal(basis.TargetValue(db.noms), &basisCommit)
	if err != nil {
		return types.Ref{}, types.String(""), nil, false, err
	}

	newData := basisCommit.Value.Data

	if strings.HasPrefix(function, ".") {
		switch function {
		case ".putValue":
			k := args.Get(0).(types.String)
			v := args.Get(1)
			ed := basisCommit.Data(db.noms).Edit()
			isWrite = true
			err = ed.Set(k, v)
			if err != nil {
				err = fmt.Errorf("could not Put '%s'='%s': %w", k, v, err)
				return
			}
			newMap := ed.Build()
			newDataChecksum = newMap.NomsChecksum()
			newData = db.noms.WriteValue(newMap.NomsMap())
			break

		case ".delValue":
			k := args.Get(0).(types.String)
			m := basisCommit.Data(db.noms)
			ed := m.Edit()
			isWrite = true
			ok := ed.Has(k)
			err = ed.Remove(k)
			if err != nil {
				err = fmt.Errorf("could not Del '%s': %w", k, err)
				return
			}
			newMap := ed.Build()
			newData = db.noms.WriteValue(newMap.NomsMap())
			newDataChecksum = newMap.NomsChecksum()
			output = types.Bool(ok)
			break
		}
	} else {
		d.Panic("NON-INTERNAL TRANSACTIONS DISABLED FOR NOW")
	}

	return newData, newDataChecksum, output, isWrite, nil
}

func (db *DB) lock() func() {
	db.mu.Lock()
	return func() {
		db.mu.Unlock()
	}
}
