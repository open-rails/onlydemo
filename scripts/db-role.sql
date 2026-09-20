-- Local Docker development only. This login inherits OpenRails' runtime grants
-- without bypassing merchant row-level security; migrations still use postgres.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'openrails_demo_billing') THEN
        CREATE ROLE openrails_demo_billing LOGIN PASSWORD 'local-demo-billing'
            NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS;
    END IF;
END $$;
GRANT openrails_app TO openrails_demo_billing;
