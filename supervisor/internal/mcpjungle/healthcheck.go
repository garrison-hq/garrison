package mcpjungle

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// HealthCheck verifies MCPJungle is reachable + the admin token is
// accepted. 5-second timeout. Returns nil on 200; ErrAdminTokenInvalid
// on 401; ErrUnreachable on any connection-level failure. Called by
// the supervisor at startup with degrade-with-warning posture per
// FR-308 — failure logs a warning and the supervisor continues; per-
// spawn MCPJungle calls then fail individually with typed errors.
func (c *Client) HealthCheck(ctx context.Context) error {
	hctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	resp, err := c.do(hctx, http.MethodGet, "/health", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized:
		return ErrAdminTokenInvalid
	default:
		return fmt.Errorf("mcpjungle: HealthCheck returned status %d", resp.StatusCode)
	}
}
