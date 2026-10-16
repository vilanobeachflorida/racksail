package notifyapi

import (
	"net/http"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/humatest"

	"github.com/vilanobeachflorida/racksail/internal/modules"
)

// The summary and description of an operation document the API, label the
// CLI's help and describe the assistant's tools, so none goes without.
func TestOperationsAreDescribed(t *testing.T) {
	_, a := humatest.New(t)
	register(a, &modules.Services{})
	count := 0
	for path, item := range a.OpenAPI().Paths {
		for method, op := range map[string]*huma.Operation{http.MethodGet: item.Get, http.MethodPost: item.Post, http.MethodPut: item.Put, http.MethodDelete: item.Delete} {
			if op == nil {
				continue
			}
			count++
			if op.Summary == "" || op.Description == "" {
				t.Errorf("%s %s (%s) needs a summary and a description", method, path, op.OperationID)
			}
		}
	}
	if count == 0 {
		t.Fatal("no operations were registered")
	}
}
