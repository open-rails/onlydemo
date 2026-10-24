package main

import (
	"context"
	"github.com/open-rails/contentkit"
	ckopenrails "github.com/open-rails/contentkit/adapters/openrails"
	riverkit "github.com/open-rails/helpers/river"
)

// ContentKit owns the inbox receipt and cursor in one transaction. OpenRails
// retains source events independently; the host registers no consumer or ACK.
type notifications struct{ contribution riverkit.Contribution }

func newNotifications(ctx context.Context, cfg Config, billing *billingService, auth *appAuth, content *contentkit.Engine) (*notifications, error) {
	source, err := ckopenrails.NewNotificationSource(billing.client, "openrails:onlydemo")
	if err != nil {
		return nil, err
	}
	jobs, err := content.OpenRailsNotificationsContext(ctx, source, ckopenrails.SameAccounts(auth.client.UserInfo(), cfg.AuthIssuer))
	if err != nil {
		return nil, err
	}
	return &notifications{contribution: jobs}, nil
}
func (n *notifications) RiverJobs() riverkit.Contribution { return n.contribution }
