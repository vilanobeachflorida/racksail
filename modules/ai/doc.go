// Package ai is the optional assistant: a chat docked beside every screen,
// backed by local models through llama.cpp or by a provider the admin connects
// (any OpenAI-compatible API, Anthropic or Google Gemini). Its tools are
// generated from the API with the signed-in user's permissions, and every
// change it proposes is a change set the user reviews. It also serves the API
// to outside assistant apps over MCP and runs diagnostics.
//
// Its screens live in ui/src/modules/ai and its documentation in
// docs/site/src/content/docs/modules/ai.md.
package ai
