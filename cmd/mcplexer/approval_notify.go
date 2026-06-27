package main

import (
	"time"

	"github.com/don-works/mcplexer/internal/notify"
)

// approvalNotifyAdapter bridges approval.Manager → notify.Bus so pending
// and resolved approvals light up the dashboard Signal tray + the OS
// notification path. Pre-fix, approvals only published to approval.Bus
// (the /api/v1/approvals/stream SSE channel) which the Signal tray
// component never subscribes to — so a Bash command awaiting human
// approval was effectively invisible unless you happened to be staring
// at the Approvals page. Mirror of cmd/mcplexer/secret_prompts.go's
// notifyBusAdapter, generalised to the publisher signature
// approval.Manager.SetNotifyPublisher expects.
type approvalNotifyAdapter struct{ bus *notify.Bus }

func (a *approvalNotifyAdapter) Publish(
	messageID, agentName, role, kind, priority, title, body, tags, link string,
) {
	if a == nil || a.bus == nil {
		return
	}
	a.bus.Publish(notify.Event{
		MessageID: messageID,
		Source:    "approval",
		AgentName: agentName,
		Role:      role,
		Kind:      kind,
		Priority:  priority,
		Title:     title,
		Body:      body,
		Tags:      tags,
		Link:      link,
		CreatedAt: time.Now().UTC(),
	})
}
