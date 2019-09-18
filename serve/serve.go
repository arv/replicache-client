// Package serve implements the Replicant http server. This includes all the Noms endpoints,
// plus a Replicant-specific sync endpoint that implements the server-side of the Replicant sync protocol.
package serve

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime/debug"
	"strings"
	"sync"

	"github.com/attic-labs/noms/go/chunks"
	"github.com/attic-labs/noms/go/datas"
	"github.com/attic-labs/noms/go/hash"
	"github.com/attic-labs/noms/go/marshal"
	"github.com/attic-labs/noms/go/util/verbose"
	"github.com/julienschmidt/httprouter"

	"github.com/aboodman/replicant/api"
	"github.com/aboodman/replicant/db"
)

// Server is a single Replicant instance. The Replicant service runs many such instances.
type Server struct {
	router *httprouter.Router
	db     *db.DB
	mu     sync.Mutex
	api    *api.API
}

func NewServer(cs chunks.ChunkStore, urlPrefix, origin string) (*Server, error) {
	router := datas.Router(cs, urlPrefix)
	noms := datas.NewDatabase(cs)
	db, err := db.New(noms, origin)
	if err != nil {
		return nil, err
	}
	s := &Server{router: router, db: db, api: api.New(db)}
	for _, method := range []string{"getRoot", "has", "get", "scan", "put", "del", "getBundle", "putBundle", "exec"} {
		m := method
		s.router.POST(fmt.Sprintf("%s/%s", urlPrefix, method), func(w http.ResponseWriter, req *http.Request, ps httprouter.Params) {
			body := bytes.Buffer{}
			_, err := io.Copy(&body, req.Body)
			if err != nil {
				serverError(w, err)
				return
			}
			resp, err := s.api.Dispatch(m, body.Bytes())
			if err != nil {
				// TODO: this might not be a client (4xx) error
				// Need to change API to be able to indicate user vs server error
				clientError(w, err.Error()+"\n")
			}
			_, err = io.Copy(w, bytes.NewReader(resp))
			if err != nil {
				serverError(w, err)
			}

			w.Write([]byte{'\n'})
		})
	}
	s.router.POST(urlPrefix+"/sync", func(w http.ResponseWriter, req *http.Request, ps httprouter.Params) {
		s.sync(w, req)
	})
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	verbose.SetVerbose(true)
	fmt.Println("Handling request: ", r.URL.String())

	defer func() {
		err := recover()
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(os.Stderr, "handler panicked: %+v\n", err)
			debug.PrintStack()
		}
	}()

	s.router.ServeHTTP(w, r)
}

func (s *Server) sync(w http.ResponseWriter, req *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	err := s.db.Reload()
	if err != nil {
		serverError(w, err)
		return
	}
	params := req.URL.Query()
	clientHash, ok := hash.MaybeParse(params.Get("head"))
	if !ok {
		clientError(w, "invalid value for head param")
		return
	}
	var clientCommit db.Commit
	clientVal := s.db.Noms().ReadValue(clientHash)
	if clientVal == nil {
		clientError(w, "Specified hash not found")
		return
	}
	err = marshal.Unmarshal(clientVal, &clientCommit)
	if err != nil {
		clientError(w, "Invalid client commit")
		return
	}
	mergedCommit, err := db.HandleSync(s.db, clientCommit)
	if err != nil {
		serverError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
	io.Copy(w, strings.NewReader(mergedCommit.TargetHash().String()))
}

func clientError(w http.ResponseWriter, msg string) {
	w.WriteHeader(http.StatusBadRequest)
	fmt.Println(http.StatusBadRequest, msg)
	io.Copy(w, strings.NewReader(msg))
}

func serverError(w http.ResponseWriter, err error) {
	w.WriteHeader(http.StatusInternalServerError)
	fmt.Fprintln(os.Stderr, err.Error())
}
