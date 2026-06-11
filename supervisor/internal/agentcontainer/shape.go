package agentcontainer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// ReconcileShape (real impl) converges every addressed agent container
// to the desired create shape at boot (FR-007; plan §3). For each spec
// it inspects the container named ContainerName(spec.AgentID) and:
//
//   - missing                            → create + start          (Created)
//   - shape-hash label absent or stale   → stop, remove, create,
//     start                              (Recreated — the unlabeled
//     Exited(1) fleet lands here)
//   - hash match, not running            → start                   (Restarted)
//   - hash match, running                → no-op                   (Unchanged)
//
// Only containers addressed by the agent-ID name are ever touched —
// the chat container and other compose services are invisible to this
// walk. Per-spec failures log, accumulate into the joined error, and
// the walk continues so one broken container can't block the rest of
// the fleet; the caller degrades warn-and-continue like migrate7 and
// the next boot repairs.
func (c *socketProxyController) ReconcileShape(ctx context.Context, specs []ContainerSpec) (ShapeReport, error) {
	report := ShapeReport{}
	var errs []error
	for _, spec := range specs {
		if err := c.reconcileShapeOne(ctx, spec, &report); err != nil {
			c.logger.Warn("agentcontainer: shape reconcile failed for agent; continuing",
				"agent_id", spec.AgentID, "err", err)
			errs = append(errs, fmt.Errorf("agent %s: %w", spec.AgentID, err))
		}
	}
	return report, errors.Join(errs...)
}

func (c *socketProxyController) reconcileShapeOne(ctx context.Context, spec ContainerSpec, report *ShapeReport) error {
	desired, err := buildCreateBody(spec)
	if err != nil {
		return err
	}
	desiredHash := desired.Labels[shapeHashLabel]
	name := ContainerName(spec.AgentID)

	inspected, err := c.containerInspect(ctx, name)
	if errors.Is(err, ErrContainerNotFound) {
		if err := c.createAndStart(ctx, spec); err != nil {
			return err
		}
		report.Created = append(report.Created, spec.AgentID)
		return nil
	}
	if err != nil {
		return err
	}

	if inspected.Config.Labels[shapeHashLabel] != desiredHash {
		// Old shape (no shape_hash label at all) or a stale hash:
		// recreate. Stop is best-effort — an already-exited container
		// answers 304 (mapped to nil) and any other stop failure
		// doesn't block the forced remove.
		if err := c.Stop(ctx, inspected.ID); err != nil {
			c.logger.Warn("agentcontainer: shape reconcile stop failed; forcing remove",
				"agent_id", spec.AgentID, "err", err)
		}
		if err := c.Remove(ctx, inspected.ID); err != nil {
			return fmt.Errorf("remove old-shape container: %w", err)
		}
		if err := c.createAndStart(ctx, spec); err != nil {
			return err
		}
		report.Recreated = append(report.Recreated, spec.AgentID)
		return nil
	}

	if !inspected.State.Running {
		if err := c.Start(ctx, inspected.ID); err != nil {
			return fmt.Errorf("start stopped matching container: %w", err)
		}
		report.Restarted = append(report.Restarted, spec.AgentID)
		return nil
	}

	report.Unchanged = append(report.Unchanged, spec.AgentID)
	return nil
}

func (c *socketProxyController) createAndStart(ctx context.Context, spec ContainerSpec) error {
	id, err := c.Create(ctx, spec)
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}
	if err := c.Start(ctx, id); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	return nil
}

// containerInspectJSON is the subset of GET /containers/<id>/json the
// shape reconcile reads: identity, run state, and the labels carrying
// (or missing) the shape hash.
type containerInspectJSON struct {
	ID    string `json:"Id"`
	State struct {
		Running bool `json:"Running"`
	} `json:"State"`
	Config struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
}

func (c *socketProxyController) containerInspect(ctx context.Context, nameOrID string) (containerInspectJSON, error) {
	resp, err := c.do(ctx, http.MethodGet, containersPathPrefix+nameOrID+"/json", nil)
	if err != nil {
		return containerInspectJSON{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return containerInspectJSON{}, c.statusErr(resp, "inspect")
	}
	var out containerInspectJSON
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return containerInspectJSON{}, fmt.Errorf("agentcontainer: parse container inspect: %w", err)
	}
	return out, nil
}
