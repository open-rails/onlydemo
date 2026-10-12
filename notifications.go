package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/open-rails/contentkit/notification"
	"github.com/open-rails/helpers/notify"
	riverkit "github.com/open-rails/helpers/river"
	"github.com/open-rails/openrails"
	"github.com/open-rails/openrails/billing"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// OnlyDemo uses the same verified AuthKit subject as its billing customer ID.
// Its independent consumer stores local receipts before acknowledging facts.
type notifications struct {
	source          *openrails.NotificationConsumer
	store           *notification.Store
	auth            *appAuth
	merchant        billing.MerchantID
	includeOptional bool
}

func newNotifications(ctx context.Context, cfg Config, billing *billingService, media *mediaService) (*notifications, error) {
	source, err := billing.client.NotificationEvents("onlydemo-inbox")
	if err != nil {
		return nil, err
	}
	store, err := notification.New(notification.Options{Pool: media.pool, Schema: contentSchema(cfg), Tenant: tenant})
	if err != nil {
		return nil, err
	}
	if err = source.Register(ctx); err != nil {
		return nil, err
	}
	return &notifications{source: source, store: store, auth: media.auth, merchant: billing.client.MerchantID(), includeOptional: cfg.NotificationReceipts}, nil
}

func (n *notifications) importEvents(ctx context.Context) error {
	events, err := n.source.Pending(ctx, 100)
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(events))
	for _, event := range events {
		if !event.Optional || n.includeOptional {
			ids = append(ids, event.CustomerID.String())
		}
	}
	users, err := n.auth.client.Users(ctx, unique(ids))
	if err != nil {
		return err
	}
	ack := make([]billing.NotificationEventRef, 0, len(events))
	var failures []error
	for _, event := range events {
		if event.MerchantID != n.merchant {
			failures = append(failures, errors.New("notification source returned another merchant"))
			continue
		}
		ref := billing.NotificationEventRef{MerchantID: event.MerchantID, ID: event.ID}
		user, exists := users[event.CustomerID.String()]
		if (event.Optional && !n.includeOptional) || !exists || user.DeletedAt != nil {
			ack = append(ack, ref)
			continue
		}
		_, err := n.store.Publish(ctx, notify.Input{
			Recipient: notify.Recipient{Audience: notify.AudienceUser, ID: event.CustomerID.String()},
			Origin:    notify.Origin{System: "openrails:onlydemo", Scope: event.MerchantID.String(), ID: event.ID.String(), Subject: event.CustomerID.String()},
			Kind:      event.EventType, Data: event.Data, CreatedAt: event.CreatedAt, ActionRequested: event.ActionRequested,
		})
		if err != nil && !errors.Is(err, notify.ErrRecipientErased) {
			failures = append(failures, fmt.Errorf("import billing notice %s: %w", event.ID, err))
			continue
		}
		ack = append(ack, ref)
	}
	if len(ack) > 0 {
		failures = append(failures, n.source.Acknowledge(ctx, ack))
	}
	return errors.Join(failures...)
}

type notificationPollArgs struct{}

func (notificationPollArgs) Kind() string { return "onlydemo_notification_poll" }
func (notificationPollArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 1, UniqueOpts: river.UniqueOpts{ByState: []rivertype.JobState{
		rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning, rivertype.JobStateRetryable, rivertype.JobStateScheduled,
	}}}
}

type notificationWorker struct {
	river.WorkerDefaults[notificationPollArgs]
	notices *notifications
}

func (w *notificationWorker) Work(ctx context.Context, _ *river.Job[notificationPollArgs]) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return w.notices.importEvents(ctx)
}
func (n *notifications) RiverJobs() riverkit.Contribution {
	return riverkit.NewContribution("onlydemo-notifications", func(_ context.Context, cfg *river.Config) error {
		if cfg.Workers == nil {
			cfg.Workers = river.NewWorkers()
		}
		river.AddWorker(cfg.Workers, &notificationWorker{notices: n})
		cfg.PeriodicJobs = append(cfg.PeriodicJobs, river.NewPeriodicJob(river.PeriodicInterval(10*time.Second), func() (river.JobArgs, *river.InsertOpts) { return notificationPollArgs{}, nil }, &river.PeriodicJobOpts{RunOnStart: true}))
		return nil
	}, nil, nil)
}
