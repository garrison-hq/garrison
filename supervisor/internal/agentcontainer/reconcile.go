package agentcontainer

import (
	"context"
)

// Reconcile (real impl) compares the supervisor's expected set
// against the docker-engine's actual state and reports drift. Called
// once at supervisor startup before normal lifecycle resumes
// (FR-214 + plan §3 algorithm).
//
// Algorithm:
//  1. List actual managed containers via the proxy
//     (label garrison.managed=true).
//  2. For each expected: if found running and ExpectedRunning, adopt.
//     If found stopped and ExpectedRunning, restart.
//     If found and ExpectedRemoved, remove.
//  3. For each actual not in expected (orphan): remove.
func (c *socketProxyController) Reconcile(ctx context.Context, expected []ExpectedContainer) (ReconcileReport, error) {
	actual, err := c.listContainers(ctx)
	if err != nil {
		return ReconcileReport{}, err
	}

	expectedByID := map[string]ExpectedContainer{}
	for _, e := range expected {
		expectedByID[e.ContainerID] = e
	}

	report := ReconcileReport{}
	seenIDs := map[string]struct{}{}

	for _, a := range actual {
		seenIDs[a.ID] = struct{}{}
		exp, ok := expectedByID[a.ID]
		if !ok {
			// Orphan — no agents row references this container.
			c.logger.Info("agentcontainer: gc orphan", "container_id", a.ID, "image", a.Image)
			if err := c.Remove(ctx, a.ID); err != nil {
				report.Mismatches = append(report.Mismatches, ReconcileMismatch{
					ActualKind: a.State,
					Reason:     "orphan remove failed: " + err.Error(),
				})
				continue
			}
			report.GarbageCollected = append(report.GarbageCollected, a.ID)
			continue
		}
		switch exp.State {
		case ExpectedRunning:
			if a.State == "running" {
				report.AdoptedRunning = append(report.AdoptedRunning, exp.AgentID)
			} else {
				if err := c.Start(ctx, a.ID); err != nil {
					report.Mismatches = append(report.Mismatches, ReconcileMismatch{
						AgentID: exp.AgentID, Expected: exp.State, ActualKind: a.State,
						Reason: "restart failed: " + err.Error(),
					})
					continue
				}
				report.Restarted = append(report.Restarted, exp.AgentID)
			}
		case ExpectedStopped:
			if a.State == "running" {
				if err := c.Stop(ctx, a.ID); err != nil {
					report.Mismatches = append(report.Mismatches, ReconcileMismatch{
						AgentID: exp.AgentID, Expected: exp.State, ActualKind: a.State,
						Reason: "stop failed: " + err.Error(),
					})
				}
			}
		case ExpectedRemoved:
			if err := c.Remove(ctx, a.ID); err != nil {
				report.Mismatches = append(report.Mismatches, ReconcileMismatch{
					AgentID: exp.AgentID, Expected: exp.State, ActualKind: a.State,
					Reason: "remove failed: " + err.Error(),
				})
				continue
			}
			report.GarbageCollected = append(report.GarbageCollected, a.ID)
		}
	}

	// Expected containers that the docker daemon doesn't know about
	// — log a mismatch but don't fail reconciliation. The supervisor
	// can re-create on the next agent activation if needed.
	for _, exp := range expected {
		if _, found := seenIDs[exp.ContainerID]; !found {
			report.Mismatches = append(report.Mismatches, ReconcileMismatch{
				AgentID:    exp.AgentID,
				Expected:   exp.State,
				ActualKind: "missing",
				Reason:     "expected container not present in docker daemon",
			})
		}
	}

	return report, nil
}
