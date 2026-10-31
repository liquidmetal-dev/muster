package github

import (
	"context"
	"time"
)

// SetSleep replaces the wait between retries.
func SetSleep(c *Config, f func(context.Context, time.Duration) error) { c.sleep = f }

// TokenFor returns the token the client would send on its next call.
func TokenFor(ctx context.Context, c *Client) (string, error) { return c.src.Token(ctx) }

// SignAppJWT exposes the App JWT signer.
var SignAppJWT = signAppJWT
