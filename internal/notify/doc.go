// Package notify sends notifications through channels: email, webhook,
// Matrix, ntfy, Telegram, Discord and Slack in the core, and others through
// provide.notifier providers of modules and add-ons.
//
// A channel type registers a factory with RegisterType. Channels are built
// from the settings people save, whose secrets stay in the secrets store,
// and send Messages: a title, a body in plain text, a severity and a link
// back to the panel. Email goes out through the mail relay, the
// core.smtp_relay resource, dressed in the theme's email template. Every
// channel that posts over HTTP connects only to public addresses unless
// webhooks.allow_networks lists the network.
//
// Admins set up core.notification_channel resources, each with the events
// it wants, how urgent they have to be and, optionally, quiet hours. People
// choose for themselves which events reach them by email, with quiet hours
// of their own; they only hear about what they may see. The Service follows
// the event timeline the way webhooks do, keeping its place and every
// delivery in the panel database: deliveries that fail are tried again for
// a few hours, and what quiet hours hold back goes out as one summary when
// they end. Other parts of the panel send messages that aren't about an
// event, such as sign-in alerts and customer emails, with Service.Send.
package notify
