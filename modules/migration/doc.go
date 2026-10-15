// Package migration moves accounts in from other panels and servers: CWP,
// cPanel, DirectAdmin, HestiaCP, VestaCP, Plesk, Unraid and any SSH server,
// with a scan, a plan with warnings, a preview of every site before switching,
// DNS and mail cutover with catch-up sync, and rollback. It turns itself on
// for an import.
//
// Its screens live in ui/src/modules/migration and its documentation in
// docs/site/src/content/docs/modules/migration.md.
package migration
