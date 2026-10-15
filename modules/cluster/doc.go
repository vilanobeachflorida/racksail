// Package cluster runs Racksail on several servers: servers join with a one-
// time token over mutual TLS, get roles and labels, and receive accounts by
// capacity. It adds DNS secondaries, live account moves, warm standby pairs
// with failover and failback, a standby panel through Litestream, and rolling
// upgrades.
//
// Its screens live in ui/src/modules/cluster and its documentation in
// docs/site/src/content/docs/modules/cluster.md.
package cluster
