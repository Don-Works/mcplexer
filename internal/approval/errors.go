package approval

import "errors"

var (
	// ErrSelfApproval is returned when an agent tries to approve its own request.
	ErrSelfApproval = errors.New("cannot approve own request")

	// ErrAlreadyResolved is returned when an approval has already been resolved.
	ErrAlreadyResolved = errors.New("approval already resolved")

	// ErrApproverTypeRequired is returned when Resolve is called without an
	// approver type. The previous default of accepting an empty type let the
	// HTTP handler's hardcoded "dashboard" call short-circuit identity checks.
	ErrApproverTypeRequired = errors.New("approver type required")

	// ErrApproverIdentityRequired is returned when a dashboard resolve is
	// called without an approver session identifier. The dashboard handler
	// must derive a stable identifier (e.g. token hash) so that the
	// self-approval check can distinguish the resolver from the requester.
	ErrApproverIdentityRequired = errors.New("approver identity required")
)
