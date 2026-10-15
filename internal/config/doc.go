// Package config reads /etc/racksail/racksail.toml, the small hand-edited
// settings file: listen addresses, who may reach the panel, data paths, the
// update channel and logging. Everything else Racksail knows is runtime
// settings stored as resources.
//
// Every setting has a default, so a missing file or a file with a few lines
// is fine. Unknown keys are errors, so typos don't go unnoticed. Dev returns
// the configuration of `racksail dev`, with every path inside a data
// directory in the repository.
package config
