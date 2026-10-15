// Package notify sends notifications through channels: email, webhook,
// Matrix, ntfy, Telegram, Discord and Slack in the core, and others through
// provide.notifier add-ons.
//
// A channel type registers a factory with RegisterType. Channels are built
// from the settings people save (their secrets come from the secrets store)
// and send Messages: a title, a body in plain text, a severity and a link
// back to the panel. Alerts, login notices, customer emails and failed jobs
// all go through Notifier.
package notify
