// Package github is muster's client for the GitHub REST API on github.com
// and GitHub Enterprise Server, built on go-github. It covers JIT runner
// configuration, runner listing and deletion, runner group lookup, queued
// job listing, and the credential reach check used by validation.
//
// A Client authenticates as a GitHub App installation or with a personal
// access token. Installation tokens are refreshed 5 minutes before they
// expire. Credentials are sent only to the configured API origin.
//
// Every call goes through one retry policy: rate limited responses (429, or
// 403 with Retry-After, no remaining quota, or a secondary limit named in
// the body) are retried for any method;
// server and network errors only for GET, HEAD and DELETE, so a JIT
// configuration request is never sent twice after a possible success. A
// rate limit that asks for a wait longer than Config.MaxWait is returned as
// *RateLimitedError.
//
// See docs/adr/2026-10-31-github-client.md.
package github
