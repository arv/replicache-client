// +build js,wasm

package main

import (
	"encoding/json"
	"fmt"
	"syscall/js"

	zl "github.com/rs/zerolog"

	spec "github.com/attic-labs/noms/go/spec/lite"

	"roci.dev/replicache-client/api"
	_ "roci.dev/replicache-client/idb"
)

func init() {
	api.Register(list, resolve, drop)
}

type DatabaseInfo struct {
	Name string `json:"name"`
}

type ListResponse struct {
	Databases []DatabaseInfo `json:"databases"`
}

func list(l zl.Logger) (resBytes []byte, err error) {
	resp := ListResponse{
		Databases: []DatabaseInfo{},
	}
	return json.Marshal(resp)
}

func resolve(dbName string, l zl.Logger) (spec.Spec, error) {
	p := "idb:" + dbName
	l.Info().Msgf("Opening Replicache database '%s' at '%s'", dbName, p)
	return spec.ForDatabase(p)
}

func drop(dbName string) error {
	return fmt.Errorf("Not implemented")
}

func dispatch(this js.Value, inputs []js.Value) interface{} {
	dbName := inputs[0].String()
	rpc := inputs[1].String()
	data := inputs[2].String()
	cb := inputs[3]
	go func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Println("Recovered in f", r)
			}
		}()
		resp, err := api.Dispatch(dbName, rpc, []byte(data))
		if err != nil {
			cb.Invoke(err.Error(), js.Null())
		} else {
			cb.Invoke(js.Undefined(), string(resp))
		}
	}()
	return nil
}

func main() {
	c := make(chan bool)
	js.Global().Set("replicache",
		map[string]interface{}{
			"dispatch": js.FuncOf(dispatch),
		})
	<-c
}
