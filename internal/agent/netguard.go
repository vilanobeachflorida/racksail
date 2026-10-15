package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// A network change that could cut the panel off is applied with a deadline:
// unless it is confirmed in time, the agent puts the previous files back on
// its own, since by then nobody may be able to reach it to ask. The
// previous files are kept in the agent's state, so a restart in between
// still rolls back.

const (
	networkPendingFile = "network-pending.json"
	networkLastFile    = "network-last.json"
)

// lastNetwork is how the last change waiting for confirmation ended, so a
// confirmation sent twice is told apart from one that came too late.
type lastNetwork struct {
	Token   string    `json:"token"`
	Outcome string    `json:"outcome" enum:"confirmed,rolled_back"`
	Time    time.Time `json:"time"`
}

// Outcomes of a change waiting for confirmation.
const (
	networkConfirmed  = "confirmed"
	networkRolledBack = "rolled_back"
)

func (rt *Runtime) loadLast(ctx context.Context) (*lastNetwork, error) {
	data, err := rt.readState(ctx, networkLastFile)
	if err != nil || data == nil {
		return nil, err
	}
	var l lastNetwork
	if err := json.Unmarshal(data, &l); err != nil {
		return nil, fmt.Errorf("reading the last network change: %w", err)
	}
	return &l, nil
}

func (rt *Runtime) saveLast(ctx context.Context, token, outcome string) error {
	data, err := json.Marshal(lastNetwork{Token: token, Outcome: outcome, Time: rt.now().UTC()})
	if err != nil {
		return err
	}
	return rt.writeState(ctx, networkLastFile, data)
}

// pendingNetwork is a network change waiting for confirmation.
type pendingNetwork struct {
	Token    string            `json:"token"`
	Deadline time.Time         `json:"deadline"`
	Previous map[string]string `json:"previous"`
}

// NetworkChange describes an applied change waiting for confirmation.
type NetworkChange struct {
	Token    string    `json:"token" doc:"Pass to ConfirmNetworkConfig to keep the change"`
	Deadline time.Time `json:"deadline" doc:"When the change is rolled back unless confirmed"`
}

func (rt *Runtime) loadPending(ctx context.Context) (*pendingNetwork, error) {
	data, err := rt.readState(ctx, networkPendingFile)
	if err != nil || data == nil {
		return nil, err
	}
	var p pendingNetwork
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("reading the pending network change: %w", err)
	}
	return &p, nil
}

// applyNetwork applies files and arms the rollback.
func (rt *Runtime) applyNetwork(ctx context.Context, files map[string][]byte, within time.Duration) (NetworkChange, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	pending, err := rt.loadPending(ctx)
	if err != nil {
		return NetworkChange{}, err
	}
	if pending != nil {
		return NetworkChange{}, ErrConflict.New("op", "ApplyNetworkConfig", "reason", "a network change is waiting to be confirmed until "+pending.Deadline.Format(time.RFC3339))
	}
	previous, err := rt.backend.Network.Config(ctx)
	if err != nil {
		return NetworkChange{}, err
	}
	p := pendingNetwork{Token: newToken(), Deadline: rt.now().Add(within).UTC(), Previous: map[string]string{}}
	for name, data := range previous {
		p.Previous[name] = string(data)
	}
	data, err := json.Marshal(p)
	if err != nil {
		return NetworkChange{}, err
	}
	// The way back is saved before anything changes.
	if err := rt.writeState(ctx, networkPendingFile, data); err != nil {
		return NetworkChange{}, err
	}
	if err := rt.backend.Network.ApplyConfig(ctx, files); err != nil {
		if rerr := rt.backend.Network.ApplyConfig(ctx, previous); rerr != nil {
			rt.log.Error("putting the previous network configuration back", "error", rerr)
		}
		rt.removeState(ctx, networkPendingFile)
		return NetworkChange{}, err
	}
	rt.armRollback(p.Deadline)
	return NetworkChange{Token: p.Token, Deadline: p.Deadline}, nil
}

// armRollback starts the timer. The caller holds rt.mu.
func (rt *Runtime) armRollback(deadline time.Time) {
	if rt.rollback != nil {
		rt.rollback.Stop()
	}
	rt.rollback = time.AfterFunc(max(time.Until(deadline), 0), func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := rt.rollbackNetwork(ctx); err != nil {
			rt.log.Error("rolling back the network change", "error", err)
		}
	})
}

// confirmNetwork keeps a pending change, reporting whether it was still
// waiting. Confirming a change that was already kept changes nothing; one
// that was rolled back, or anything without a change waiting, is a
// conflict, so a confirmation that came too late is never taken for one
// that worked. In check mode nothing changes.
func (rt *Runtime) confirmNetwork(ctx context.Context, token string, check bool) (bool, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	pending, err := rt.loadPending(ctx)
	if err != nil {
		return false, err
	}
	if pending == nil {
		last, err := rt.loadLast(ctx)
		switch {
		case err != nil:
			return false, err
		case last != nil && last.Token == token && last.Outcome == networkConfirmed:
			return false, nil
		case last != nil && last.Token == token && last.Outcome == networkRolledBack:
			return false, ErrConflict.New("op", "ConfirmNetworkConfig", "reason",
				"the change wasn't confirmed in time and was rolled back at "+last.Time.Format(time.RFC3339))
		}
		return false, ErrConflict.New("op", "ConfirmNetworkConfig", "reason", "no network change is waiting to be confirmed")
	}
	if pending.Token != token {
		return false, ErrConflict.New("op", "ConfirmNetworkConfig", "reason", "the token is for another network change")
	}
	if check {
		return true, nil
	}
	if rt.rollback != nil {
		rt.rollback.Stop()
		rt.rollback = nil
	}
	if err := rt.saveLast(ctx, token, networkConfirmed); err != nil {
		return false, err
	}
	if err := rt.removeState(ctx, networkPendingFile); err != nil {
		return false, err
	}
	rt.events.publish(Event{Type: EventNetworkConfirmed, Time: rt.now().UTC()})
	return true, nil
}

// rollbackNetwork puts the previous files back.
func (rt *Runtime) rollbackNetwork(ctx context.Context) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	pending, err := rt.loadPending(ctx)
	if err != nil || pending == nil {
		return err
	}
	previous := map[string][]byte{}
	for name, content := range pending.Previous {
		previous[name] = []byte(content)
	}
	if err := rt.backend.Network.ApplyConfig(ctx, previous); err != nil {
		return err
	}
	rt.rollback = nil
	if err := rt.saveLast(ctx, pending.Token, networkRolledBack); err != nil {
		return err
	}
	if err := rt.removeState(ctx, networkPendingFile); err != nil {
		return err
	}
	rt.log.Warn("the network change wasn't confirmed in time, so the previous configuration is back")
	rt.events.publish(Event{Type: EventNetworkRolledBack, Time: rt.now().UTC(), Message: "The network change wasn't confirmed in time and was rolled back."})
	return nil
}

// recoverNetwork handles a change left pending by an earlier run.
func (rt *Runtime) recoverNetwork(ctx context.Context) error {
	rt.mu.Lock()
	pending, err := rt.loadPending(ctx)
	if err != nil || pending == nil {
		rt.mu.Unlock()
		return err
	}
	if rt.now().Before(pending.Deadline) {
		rt.armRollback(pending.Deadline)
		rt.mu.Unlock()
		return nil
	}
	rt.mu.Unlock()
	return rt.rollbackNetwork(ctx)
}

// networkStatus reports the pending change, if any.
func (rt *Runtime) networkStatus(ctx context.Context) (*NetworkChange, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	pending, err := rt.loadPending(ctx)
	if err != nil || pending == nil {
		return nil, err
	}
	return &NetworkChange{Token: pending.Token, Deadline: pending.Deadline}, nil
}

// networkFiles converts the input files to what the adapter takes.
func networkFiles(files map[string]string) map[string][]byte {
	out := make(map[string][]byte, len(files))
	for name, content := range files {
		out[name] = []byte(content)
	}
	return out
}
