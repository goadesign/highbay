// This file validates immutable snapshot metadata saved beside a revision's
// confirmed content. The metadata deliberately stays outside request_revision.
package controlplane

import (
	"errors"
	"fmt"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	"github.com/goadesign/highbay/internal/designversion"
)

// savedArchitecture retains the evaluated facts a preview cannot derive from
// the card. Change and revision IDs come from that card, and the preview banner
// comes from designeval.Result.Summary. Empty topology remains valid for a
// Story-only design; preview's domain checks apply when it is requested.
type savedArchitecture struct {
	// DesignDigest identifies the snapshot whose evaluation produced these facts.
	DesignDigest string `json:"design_digest"`
	// SystemName is the evaluated API, model system or primary service name.
	SystemName string `json:"system_name"`
	// Containers are the evaluated C4 nodes, in their declared order.
	Containers []*genhighbay.C4ContainerItem `json:"containers"`
	// Edges are the evaluated directed relationships between those nodes.
	Edges []*genhighbay.C4EdgeItem `json:"edges"`
	// AgentPolicies are the policies the evaluated design declares.
	AgentPolicies []*genhighbay.AgentPolicyItem `json:"agent_policies"`
	// Tools are the evaluated model-facing tool contracts.
	Tools []*genhighbay.ToolContractItem `json:"tools"`
}

// validateRevisionMetadata accepts older revisions without snapshot metadata
// or a complete snapshot group. It validates saved structure independently of
// today's guide draft, so a later Drop cannot invalidate an older revision.
func validateRevisionMetadata(
	architecture *savedArchitecture, goMod string, madeFrom *madeFromSnapshot,
) error {
	if architecture == nil && goMod == "" && madeFrom == nil {
		return nil
	}
	if architecture == nil || goMod == "" || madeFrom == nil {
		return errors.New("architecture, go_mod and made_from must be present together")
	}
	if !cardFileDigestPattern.MatchString(architecture.DesignDigest) {
		return errors.New("architecture has an invalid design digest")
	}
	if _, err := designversion.Requirements(goMod); err != nil {
		return fmt.Errorf("saved go.mod: %w", err)
	}
	if err := validateMadeFromSnapshot(*madeFrom); err != nil {
		return fmt.Errorf("saved provenance: %w", err)
	}
	// A pointer slice may be empty, but an explicit null item is not an
	// evaluated declaration and would make later consumers dereference nil.
	for _, item := range architecture.Containers {
		if item == nil {
			return errors.New("architecture has a null container")
		}
	}
	for _, item := range architecture.Edges {
		if item == nil {
			return errors.New("architecture has a null edge")
		}
	}
	for _, item := range architecture.AgentPolicies {
		if item == nil {
			return errors.New("architecture has a null agent policy")
		}
	}
	for _, item := range architecture.Tools {
		if item == nil {
			return errors.New("architecture has a null tool contract")
		}
	}
	return nil
}
