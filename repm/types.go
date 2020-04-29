// Package api implements the high-level API that is exposed to clients.
// Since we have many clients in many different languages, this is implemented
// language/host-indepedently, and further adapted by different packages.
package repm

import (
	"encoding/json"

	"roci.dev/replicache-client/db"

	jsnoms "roci.dev/diff-server/util/noms/json"
)

type getRootRequest struct {
}

type getRootResponse struct {
	Root jsnoms.Hash `json:"root"`
}

type hasRequest struct {
	transactionRequest
	Key string `json:"key"`
}

type hasResponse struct {
	Has bool `json:"has"`
}

type getRequest struct {
	transactionRequest
	Key string `json:"key"`
}

type getResponse struct {
	Has   bool            `json:"has"`
	Value json.RawMessage `json:"value,omitempty"`
}

type scanRequest struct {
	transactionRequest
	db.ScanOptions
}

type scanItem struct {
	Key   string       `json:"key"`
	Value jsnoms.Value `json:"value"`
}

type scanResponse struct {
	Values []scanItem `json:"values"`
	Done   bool       `json:"done"`
}

type putRequest struct {
	transactionRequest
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

type putResponse struct{}

type delRequest struct {
	transactionRequest
	Key string `json:"key"`
}

type delResponse struct {
	Ok bool `json:"ok"`
}

type pullRequest struct {
	Remote         jsnoms.Spec `json:"remote"`
	ClientViewAuth string      `json:"clientViewAuth"`
}

type pullResponseError struct {
	BadAuth string `json:"badAuth"`
}

type pullResponse struct {
	Error *pullResponseError `json:"error,omitempty"`
	Root  jsnoms.Hash        `json:"root,omitempty"`
}

type pullProgressRequest struct {
}

type pullProgressResponse struct {
	BytesReceived uint64 `json:"bytesReceived"`
	BytesExpected uint64 `json:"bytesExpected"`
}

type openTransactionRequest struct {
	Name string          `json:"name,omitempty"`
	Args json.RawMessage `json:"args,omitempty"`
}

type openTransactionResponse struct {
	TransactionID int `json:"transactionId"`
}

type transactionRequest struct {
	TransactionID int `json:"transactionId"`
}

type closeTransactionRequest transactionRequest

type closeTransactionResponse struct {
}

type commitTransactionRequest transactionRequest

type commitTransactionResponse struct {
	Ref *jsnoms.Hash `json:"ref,omitempty"`
}
