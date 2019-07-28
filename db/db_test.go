package db

import (
	"strings"
	"testing"

	"github.com/attic-labs/noms/go/spec"
	"github.com/attic-labs/noms/go/types"
	"github.com/stretchr/testify/assert"
)

func reloadDB(assert *assert.Assertions, dir string) (db *DB) {
	sp, err := spec.ForDatabase(dir)
	assert.NoError(err)

	db, err = Load(sp, "o1")
	assert.NoError(err)

	return db
}

func TestGenesis(t *testing.T) {
	assert := assert.New(t)

	db, _ := LoadTempDB(assert)

	assert.False(db.Has("foo"))
	b, err := db.Bundle()
	assert.NoError(err)
	assert.False(b == types.Blob{})
	assert.Equal(uint64(0), b.Len())

	assert.True(db.head.Original.Equals(makeGenesis(db.noms).Original))
}

func TestData(t *testing.T) {
	assert := assert.New(t)
	db, dir := LoadTempDB(assert)

	exp := types.String("bar")
	err := db.Put("foo", exp)
	assert.NoError(err)

	dbs := []*DB{
		db, reloadDB(assert, dir),
	}

	for _, d := range dbs {
		ok, err := d.Has("foo")
		assert.NoError(err)
		assert.True(ok)
		act, err := d.Get("foo")
		assert.NoError(err)
		assert.True(act.Equals(exp))

		ok, err = d.Has("bar")
		assert.NoError(err)
		assert.False(ok)

		act, err = d.Get("bar")
		assert.NoError(err)
		assert.Nil(act)
	}
}

func TestBundle(t *testing.T) {
	assert := assert.New(t)
	db, dir := LoadTempDB(assert)

	exp := types.NewBlob(db.noms, strings.NewReader("bundlebundle"))

	err := db.PutBundle(exp)
	assert.NoError(err)

	dbs := []*DB{db, reloadDB(assert, dir)}
	for _, d := range dbs {
		act, err := d.Bundle()
		assert.NoError(err)
		assert.True(exp.Equals(act))
	}
}

func TestExec(t *testing.T) {
	assert := assert.New(t)
	db, dir := LoadTempDB(assert)

	code := `function append(k, s) {
	var val = db.get(k) || [];
	val.push(s);
	db.put(k, val);
}
`

	db.PutBundle(types.NewBlob(db.noms, strings.NewReader(code)))

	err := db.Exec("append", types.NewList(db.noms, types.String("log"), types.String("foo")))
	assert.NoError(err)
	err = db.Exec("append", types.NewList(db.noms, types.String("log"), types.String("bar")))
	assert.NoError(err)

	dbs := []*DB{db, reloadDB(assert, dir)}
	for _, d := range dbs {
		act, err := d.Get("log")
		assert.NoError(err)
		assert.True(types.NewList(d.noms, types.String("foo"), types.String("bar")).Equals(act))
	}
}
