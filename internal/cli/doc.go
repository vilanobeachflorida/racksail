// Package cli builds the racksail command's client commands: one for every
// operation of the panel's API that API tokens may use, named after the
// kinds and operations of the operations catalog, with flags made from each
// operation's parameters and request body. It keeps the profiles that say
// which panel to call with which token, prints answers as tables, JSON or
// YAML, previews change sets and follows jobs while they run.
//
// The commands are built from the API document of the binary itself, so a
// module's kinds and operations get commands as soon as it registers them.
package cli
