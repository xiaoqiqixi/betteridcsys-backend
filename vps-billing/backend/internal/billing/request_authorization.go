package billing

import (
	"context"
	"net/http"
	"slices"
	"sync"
)

type requestAuthorizationKey struct{}
type requestPolicyResult struct {
	policy staffPolicy
	err    error
}
type requestAuthorization struct {
	app      *App
	once     sync.Once
	user     User
	err      error
	mu       sync.Mutex
	policies map[string]requestPolicyResult
}

func (a *App) authorizationMemo(ctx context.Context) *requestAuthorization {
	memo, _ := ctx.Value(requestAuthorizationKey{}).(*requestAuthorization)
	if memo != nil && memo.app == a {
		return memo
	}
	return nil
}

// This memo lives only for one invocation of Handler. It does not replace the
// session/permission checks under transactional locks in mutation methods.
func (a *App) current(r *http.Request) (User, error) {
	if memo := a.authorizationMemo(r.Context()); memo != nil {
		memo.once.Do(func() { memo.user, memo.err = a.currentUncached(r) })
		return memo.user, memo.err
	}
	return a.currentUncached(r)
}

func (a *App) requestStaffPolicy(ctx context.Context, id string) (staffPolicy, error) {
	memo := a.authorizationMemo(ctx)
	if memo == nil {
		return a.Store.StaffPolicy(ctx, id)
	}
	memo.mu.Lock()
	defer memo.mu.Unlock()
	result, ok := memo.policies[id]
	if !ok {
		result.policy, result.err = a.Store.StaffPolicy(ctx, id)
		if memo.policies == nil {
			memo.policies = make(map[string]requestPolicyResult)
		}
		memo.policies[id] = result
	}
	result.policy.Permissions = slices.Clone(result.policy.Permissions)
	return result.policy, result.err
}
