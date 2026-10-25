package main

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/jackc/pgx/v5"
	"github.com/open-rails/authkit"
	"github.com/open-rails/authkit/notify"
	"github.com/open-rails/authkit/userinfo"
	"github.com/open-rails/openrails"
	orauthkit "github.com/open-rails/openrails/adapters/authkit"
	"github.com/open-rails/openrails/billing"
)

// Keep the existing inbox beside the catalog, with the same durable namespace.
// OnlyDemo has one account tenant and one app; both producers are tenant-visible.
func notificationConfig(cfg Config, merchant billing.MerchantID) *authkit.NotificationsConfig {
	c := &authkit.NotificationsConfig{
		Schema:        contentSchema(cfg),
		Scopes:        map[string]string{cfg.AuthTenantID.String(): "contentkit:" + tenant},
		LegacyOrigins: []notify.OriginScope{{System: "contentkit", Scope: tenant}},
		Producers: []authkit.NotificationProducer{
			{Name: "contentkit", TenantID: cfg.AuthTenantID, Origin: notify.OriginScope{System: "contentkit", Scope: tenant}, Visibility: notify.VisibilityTenant},
			{Name: "authkit", TenantID: cfg.AuthTenantID, Origin: notify.OriginScope{System: "authkit", Scope: cfg.AuthTenantID.String()}, Visibility: notify.VisibilityTenant},
		},
	}
	if !merchant.IsZero() {
		origin := notify.OriginScope{System: "openrails:onlydemo", Scope: merchant.String()}
		c.LegacyOrigins = append(c.LegacyOrigins, origin)
		c.Producers = append(c.Producers, authkit.NotificationProducer{Name: "openrails", TenantID: cfg.AuthTenantID, Origin: origin, Visibility: notify.VisibilityTenant})
	}
	return c
}

// OpenRails resolves the persisted merchant before AuthKit can bind its exact
// notification origin. These two native dependencies fail closed during that
// short construction window. No worker or HTTP handler sees an unbound host.
type billingAccounts struct {
	current atomic.Pointer[billingAccountBinding]
}

type billingAccountBinding struct {
	directory userinfo.Lookup
	channel   *openrails.NotificationChannel
}

func (b *billingAccounts) bind(accounts *authkit.Client, merchant billing.MerchantID, issuer string) error {
	if merchant.IsZero() || issuer == "" {
		return errors.New("billing accounts require the resolved merchant and native issuer")
	}
	sink, err := accounts.Notifications("openrails")
	if err != nil {
		return err
	}
	b.current.Store(&billingAccountBinding{directory: accounts.UserInfo(), channel: orauthkit.Notifications(sink, merchant, issuer)})
	return nil
}

func (b *billingAccounts) ready() (*billingAccountBinding, error) {
	if current := b.current.Load(); current != nil {
		return current, nil
	}
	return nil, errors.New("billing account directory and notifications are not bound")
}

func (b *billingAccounts) Get(ctx context.Context, ids []string) (map[string]userinfo.User, error) {
	current, err := b.ready()
	if err != nil {
		return nil, err
	}
	return current.directory.Get(ctx, ids)
}

func (b *billingAccounts) Search(ctx context.Context, query string, limit int) ([]userinfo.User, error) {
	current, err := b.ready()
	if err != nil {
		return nil, err
	}
	return current.directory.Search(ctx, query, limit)
}

func (b *billingAccounts) Publish(ctx context.Context, publications []notify.Publication, tx ...pgx.Tx) error {
	current, err := b.ready()
	if err != nil {
		return err
	}
	return current.channel.Sink.Publish(ctx, publications, tx...)
}

func (b *billingAccounts) Withdraw(ctx context.Context, withdrawals []notify.Withdrawal, tx ...pgx.Tx) error {
	current, err := b.ready()
	if err != nil {
		return err
	}
	return current.channel.Sink.Withdraw(ctx, withdrawals, tx...)
}

func (b *billingAccounts) recipient(ctx context.Context, source openrails.NotificationRecipient) (string, bool, error) {
	current, err := b.ready()
	if err != nil {
		return "", false, err
	}
	return current.channel.Recipient(ctx, source)
}
