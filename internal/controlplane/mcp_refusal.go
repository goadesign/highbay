// This file discloses declared working-copy refusal facts through the native
// MCP adapter hook without changing the original error's classification.
package controlplane

import (
	genhighbay "github.com/goadesign/highbay/gen/highbay"
	goa "goa.design/goa/v3/pkg"
)

// mapMCPRefusal exposes a typed refusal's name and complete authored message.
// The generated adapter otherwise uses the type's fixed Error description,
// which contains neither this request's facts nor its recovery goal. err is
// non-nil, as required by the adapter hook. Every other failure stays intact.
func mapMCPRefusal(err error) error {
	switch refusal := requestErrorOwner(err).(type) {
	case *genhighbay.CopyOffBranch:
		return &goa.ServiceError{Name: refusal.Name, Message: refusal.Name + ": " + refusal.Message}
	case *genhighbay.OpenBuildRefusal:
		return &goa.ServiceError{Name: refusal.Name, Message: refusal.Name + ": " + refusal.Message}
	default:
		return err
	}
}
