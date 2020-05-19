// +build !js,!wasm

package db

import (
	"io/ioutil"

	"github.com/attic-labs/noms/go/spec"
)

func LoadTempDB() (r *DB, dir string, err error) {
	dir, err = ioutil.TempDir("", "")
	if err != nil {
		return
	}

	sp, err := spec.ForDatabase(dir)
	if err != nil {
		return
	}

	r, err = Load(sp)
	if err != nil {
		return
	}

	return
}
