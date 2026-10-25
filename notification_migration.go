package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-rails/authkit/notify"
	ckopenrails "github.com/open-rails/contentkit/adapters/openrails"
	"github.com/open-rails/contentkit/migration/legacyinbox"
	legacybilling "github.com/open-rails/contentkit/notification/openrails"
	"github.com/open-rails/openrails/billing"
)

// migrateNotifications is explicit stopped-writer maintenance, never a worker.
// AuthKit installs its optional inbox before ContentKit adopts comment receipts;
// this final billing handoff preserves exact read/archive state in that inbox.
func migrateNotifications(ctx context.Context, snapshotPath string) error {
	cfg, pool, err := openDatabase(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	s, err := compose(ctx, cfg, pool, billingOptions{})
	if err != nil {
		return err
	}
	defer s.Close()
	return adoptBillingNotifications(ctx, pool, cfg, s, snapshotPath)
}

func adoptBillingNotifications(ctx context.Context, pool *pgxpool.Pool, cfg Config, s *server, snapshotPath string) error {
	file, err := os.OpenFile(snapshotPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("create notification adoption snapshot: %w", err)
	}
	defer file.Close()
	source, err := ckopenrails.NewNotificationSource(s.billing.client, "openrails:onlydemo")
	if err != nil {
		return err
	}
	inbox, err := legacyinbox.New(legacyinbox.Options{Pool: pool, Schema: contentSchema(cfg), Tenant: tenant})
	if err != nil {
		return err
	}
	// Refuse an active old worker before consuming any of its pending facts.
	if err := source.RetireInboxSync(ctx, pool, riverSchema(cfg), inbox.Namespace(), inbox); err != nil && !errors.Is(err, legacybilling.ErrInboxHandoffRequired) {
		return err
	}
	for {
		result, err := source.Consume(ctx, inbox, ckopenrails.SameAccounts(s.auth.client.UserInfo(), cfg.AuthIssuer), legacybilling.ConsumeOptions{Filter: legacybilling.BillingLifecycle})
		if err != nil {
			return fmt.Errorf("finish legacy notification handoff: %w", err)
		}
		if !result.HasMore {
			break
		}
	}
	local, err := s.billing.engine.LocalMerchant(s.billing.client.MerchantID())
	if err != nil {
		return err
	}
	snapshot, err := local.ExportNotificationAdoption(ctx)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(file).Encode(snapshot); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(snapshotPath))
	if err != nil {
		return err
	}
	err = dir.Sync()
	closeErr := dir.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	adoptions, err := onlyDemoAdoptions(snapshot)
	if err != nil {
		return err
	}
	for batch := range slices.Chunk(adoptions, notify.MaxMutations) {
		if err := s.auth.client.AdoptNotifications(ctx, "openrails", batch); err != nil {
			return err
		}
	}
	return source.RetireInboxSync(ctx, pool, riverSchema(cfg), inbox.Namespace(), inbox)
}

// OpenRails exports its default source name. The old OnlyDemo importer used
// this explicit name instead; preserve that existing producer identity.
func onlyDemoAdoptions(snapshot billing.NotificationAdoption) ([]notify.Adoption, error) {
	if snapshot.MerchantID.IsZero() {
		return nil, fmt.Errorf("notification adoption needs the persisted merchant")
	}
	out := slices.Clone(snapshot.Adoptions)
	relabel := func(origin *notify.Origin) error {
		if origin.System != "openrails" || origin.Scope != snapshot.MerchantID.String() {
			return fmt.Errorf("notification adoption contains a foreign producer")
		}
		origin.System = "openrails:onlydemo"
		return nil
	}
	for i := range out {
		out[i].Previous = slices.Clone(out[i].Previous)
		if err := relabel(&out[i].Current.Origin); err != nil {
			return nil, err
		}
		for j := range out[i].Previous {
			if err := relabel(&out[i].Previous[j]); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}
