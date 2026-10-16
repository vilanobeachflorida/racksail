// Package config reads /etc/racksail/racksail.toml, the small hand-edited
// settings file: listen addresses, who may reach the panel and how often,
// how long records are kept, the update channel and logging. Everything
// else Racksail knows is runtime settings stored as resources.
//
// Every setting has a default, so a missing file or a file with a few lines
// is fine. Unknown keys are errors, so typos don't go unnoticed, and values
// are checked before anything starts. Where Racksail keeps its files, and
// where the agent and the edge listen for the panel, aren't settings: the
// agent, the installer, the systemd units and the add-ons' sandboxes only
// know the standard locations, so the file refuses [paths], agent.socket
// and edge.socket. Durations are written like 90d or 12h (Duration). Dev
// returns the configuration of `racksail dev`, with every path and socket
// inside a data directory in the repository.
package config
