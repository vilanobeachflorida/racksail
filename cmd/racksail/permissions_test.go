package main

import (
	"strings"
	"testing"

	"github.com/vilanobeachflorida/racksail/internal/api"
	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/modules"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// Every operation of the binary, every module's included, needs a
// permission the authorizer checks or is in the permission matrix as public
// or signed-in only, with a reason. The lists name no operation that
// doesn't exist.
func TestEveryOperationIsInThePermissionMatrix(t *testing.T) {
	if err := auth.Err(); err != nil {
		t.Fatal(err)
	}
	server := api.New(api.Options{Modules: modules.Default(), Services: &modules.Services{}})
	ops := server.Catalog()
	if problems := api.CheckMatrix(ops, store.Kinds().All()); len(problems) > 0 {
		t.Errorf("operations the permission matrix doesn't cover:\n%s", strings.Join(problems, "\n"))
	}
	exists := map[string]bool{}
	for _, op := range ops {
		exists[op.ID] = true
	}
	for _, list := range []map[string]string{api.PublicOperations, api.SignedInOperations} {
		for id, reason := range list {
			if !exists[id] {
				t.Errorf("%s is in the permission matrix but no operation has that ID", id)
			}
			if reason == "" {
				t.Errorf("%s is in the permission matrix without a reason", id)
			}
		}
	}
	// Every role has a say on every permission.
	for _, row := range server.PermissionMatrix() {
		if len(row.Roles) != len(auth.Roles()) || row.Description == "" {
			t.Errorf("matrix row %+v", row)
		}
	}
}
