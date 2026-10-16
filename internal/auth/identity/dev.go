package identity

import (
	"context"
	"net/http"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// DeveloperName is the user racksail dev signs everyone in as.
const DeveloperName = "developer"

// DevAuthenticator signs every request in as one user unless it carries a
// session or token of its own, as racksail dev does with its developer, so
// the panel's own screens work with a real user while signing in, tokens
// and impersonation can still be tried. A token that isn't valid is still
// refused.
type DevAuthenticator struct {
	Service *Service
	User    store.ID
}

var _ auth.Authenticator = DevAuthenticator{}

// Authenticate implements auth.Authenticator.
func (d DevAuthenticator) Authenticate(ctx context.Context, r *http.Request) (*auth.Actor, error) {
	a, err := d.Service.Authenticate(ctx, r)
	switch {
	case err == nil && a != nil:
		return a, nil
	case err != nil && r.Header.Get("Authorization") != "":
		return nil, err
	}
	return d.Service.Actor(ctx, d.User, d.Service.clientOf(ctx, r))
}

// Developer makes sure racksail dev's developer exists, an admin who may
// impersonate, and returns its ID.
func (s *Service) Developer(ctx context.Context) (store.ID, error) {
	u, err := s.EnsureUser(ctx, DeveloperName, firstAdminGrants())
	if err != nil {
		return "", err
	}
	return u.ID, nil
}
