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

-- This application owns River. Billing only observes and heartbeats jobs on
-- this shared fleet; enqueueing uses the host-supplied River client.
GRANT USAGE ON SCHEMA :"river_schema" TO openrails_demo_billing;
GRANT SELECT, UPDATE(attempted_at) ON TABLE :"river_schema".river_job TO openrails_demo_billing;
