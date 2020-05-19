package db

import (
	"errors"
	"testing"

	"github.com/attic-labs/noms/go/spec/lite"
	"github.com/attic-labs/noms/go/util/datetime"
	"github.com/stretchr/testify/assert"
	"roci.dev/diff-server/kv"
	"roci.dev/diff-server/util/log"
)

func TestGetSetHead(t *testing.T) {
	assert := assert.New(t)
	db, _, err := LoadTempDB()
	assert.Nil(err)
	var commits testCommits
	genesis := db.Head()
	commits = append(commits, genesis)
	commits.addLocal(assert, db, datetime.Now())

	assert.NoError(db.setHead(commits.head()))
	assert.True(db.Head().NomsStruct.Equals(commits.head().NomsStruct))
	assert.NoError(db.Reload())
	assert.True(db.Head().NomsStruct.Equals(commits.head().NomsStruct))
}

func reloadDB(assert *assert.Assertions, dir string) (db *DB) {
	sp, err := spec.ForDatabase(dir)
	assert.NoError(err)

	db, err = Load(sp)
	assert.NoError(err)

	return db
}

func TestGenesis(t *testing.T) {
	assert := assert.New(t)

	db, dir, err := LoadTempDB()
	assert.Nil(err)

	tx := db.NewTransaction()
	assert.False(tx.Has("foo"))
	v, err := tx.Get("foo")
	assert.Nil(v)
	assert.NoError(err)
	m := kv.NewMap(db.noms)
	assert.True(db.Head().NomsStruct.Equals(makeGenesis(db.noms, "", db.noms.WriteValue(m.NomsMap()), m.NomsChecksum(), 0).NomsStruct))

	cid := db.clientID
	assert.NotEqual("", cid)

	db = reloadDB(assert, dir)
	assert.Equal(cid, db.clientID)
	err = tx.Close()
	assert.NoError(err)
}

func TestData(t *testing.T) {
	assert := assert.New(t)
	db, dir, err := LoadTempDB()
	assert.Nil(err)

	exp := []byte(`"bar"`)
	tx := db.NewTransaction()
	err = tx.Put("foo", exp)
	assert.NoError(err)
	_, err = tx.Commit(log.Default())
	assert.NoError(err)

	dbs := []*DB{
		db, reloadDB(assert, dir),
	}

	for _, d := range dbs {
		tx := d.NewTransaction()
		ok, err := tx.Has("foo")
		assert.NoError(err)
		assert.True(ok)
		act, err := tx.Get("foo")
		assert.NoError(err)
		assert.Equal(exp, act, "expected %s got %s", exp, act)

		ok, err = tx.Has("bar")
		assert.NoError(err)
		assert.False(ok)

		act, err = tx.Get("bar")
		assert.NoError(err)
		assert.Nil(act)

		err = tx.Close()
		assert.NoError(err)
	}
}

func TestConflictingCommits(t *testing.T) {
	assert := assert.New(t)
	db, _, err := LoadTempDB()
	assert.Nil(err)

	tx1 := db.NewTransaction()
	err = tx1.Put("a", []byte("1"))
	assert.NoError(err)

	tx2 := db.NewTransaction()
	err = tx2.Put("b", []byte("2"))
	assert.NoError(err)

	ref1, err := tx1.Commit(log.Default())
	assert.NoError(err)
	assert.False(ref1.IsZeroValue())

	ref2, err := tx2.Commit(log.Default())
	assert.Error(err)
	var commitErrror CommitError
	assert.True(errors.As(err, &commitErrror))
	assert.True(ref2.IsZeroValue())
}
