-- RenderOps operational views for Supabase/Postgres.
--
-- Run this file once in the Supabase SQL Editor. On an empty Supabase project,
-- it first creates the current RenderOps Drizzle schema. It then creates a
-- private `ops` schema containing aggregate views plus a small statistics
-- snapshot table used to calculate table write rates.
--
-- The bootstrap definitions below mirror sandbox_platform's current Drizzle
-- schema. Drizzle migrations remain the production source of truth. This file
-- intentionally does not seed fake users, balances, policy, pricing, or usage.
--
-- Security:
--   * The ops schema is not granted to anon or authenticated roles.
--   * Views do not expose emails, API-key hashes, commands, stdout, or stderr.
--   * Supabase Studio administrators can query the views from the SQL Editor.

begin;

-- ---------------------------------------------------------------------------
-- Application schema bootstrap
-- ---------------------------------------------------------------------------

create table if not exists public."user" (
    id uuid primary key not null,
    name text not null,
    email text not null,
    email_verified boolean default false not null,
    image text,
    created_at timestamp default now() not null,
    updated_at timestamp default now() not null,
    constraint user_email_unique unique (email)
);

create table if not exists public.account (
    id uuid primary key not null,
    account_id text not null,
    provider_id text not null,
    user_id uuid not null references public."user" (id) on delete cascade,
    access_token text,
    refresh_token text,
    id_token text,
    access_token_expires_at timestamp,
    refresh_token_expires_at timestamp,
    scope text,
    password text,
    created_at timestamp default now() not null,
    updated_at timestamp default now() not null
);

create unique index if not exists account_provider_account_uniq
    on public.account (provider_id, account_id);

create table if not exists public."session" (
    id uuid primary key not null,
    expires_at timestamp not null,
    token text not null,
    created_at timestamp default now() not null,
    updated_at timestamp default now() not null,
    ip_address text,
    user_agent text,
    user_id uuid not null references public."user" (id) on delete cascade,
    constraint session_token_unique unique (token)
);

create table if not exists public.verification (
    id uuid primary key not null,
    identifier text not null,
    value text not null,
    expires_at timestamp not null,
    created_at timestamp default now() not null,
    updated_at timestamp default now() not null
);

create table if not exists public.profiles (
    id uuid primary key not null references public."user" (id) on delete cascade,
    email text not null,
    balance_usd numeric(10, 6) default 0.000000 not null,
    created_at timestamptz default now() not null,
    signup_credit_granted boolean default false not null
);

create table if not exists public.api_keys (
    id uuid primary key default gen_random_uuid() not null,
    key_hash text not null,
    key_prefix text not null,
    user_id uuid not null,
    name text not null,
    is_active boolean default true not null,
    created_at timestamptz default now() not null,
    last_used_at timestamptz,
    expires_at timestamptz
);

create table if not exists public.execution_policies (
    id text primary key not null,
    rate_limit numeric not null,
    rate_burst integer not null,
    default_exec_timeout_ms integer default 60000 not null,
    max_exec_timeout_ms integer not null,
    workers integer not null,
    pool_size integer not null,
    max_sessions integer not null,
    session_idle_timeout_ms integer not null,
    session_max_lifetime_ms integer not null,
    max_api_keys integer default 2 not null,
    created_at timestamptz default now() not null,
    updated_at timestamptz default now() not null,
    constraint execution_policies_singleton check (id = 'default')
);

create table if not exists public.pricing_rates (
    billing_model text not null,
    version integer default 1 not null,
    effective_from timestamptz not null,
    rate_vcpu_sec double precision default 0 not null,
    rate_gb_ram_sec double precision default 0 not null,
    rate_gb_disk_hour double precision default 0 not null,
    created_at timestamptz default now() not null,
    rate_execution_sec double precision default 0 not null,
    constraint pricing_rates_pkey primary key (billing_model, version)
);

create table if not exists public.sandboxes (
    id uuid primary key default gen_random_uuid() not null,
    user_id uuid not null,
    api_key_id uuid,
    name text default 'sandbox' not null,
    state text default 'active' not null,
    billing_model text default 'payg' not null,
    vcpus integer default 1 not null,
    memory_mb integer default 256 not null,
    disk_gb integer default 10 not null,
    internet boolean default true not null,
    host_id text,
    snapshot_ref text,
    created_at timestamptz default now() not null,
    updated_at timestamptz default now() not null,
    last_used_at timestamptz default now() not null,
    paused_at timestamptz,
    expires_at timestamptz,
    metadata jsonb
);

create table if not exists public.sandbox_runs (
    id uuid primary key default gen_random_uuid() not null,
    sandbox_id uuid not null,
    user_id uuid not null,
    kind text not null,
    language text,
    command text,
    exit_code integer,
    status text default 'ok' not null,
    duration_ms integer,
    started_at timestamptz default now() not null
);

create table if not exists public.sandbox_logs (
    id bigint generated always as identity primary key,
    sandbox_id uuid not null,
    user_id uuid not null,
    ts timestamptz default now() not null,
    stream text default 'stdout' not null,
    language text,
    content text,
    run_id uuid,
    level text
);

create table if not exists public.usage_logs (
    id uuid primary key default gen_random_uuid() not null,
    api_key_id uuid not null,
    user_id uuid not null,
    execution_type text not null,
    language text not null,
    duration_ms integer not null,
    exit_code integer not null,
    cost_usd numeric(10, 6) not null,
    created_at timestamptz default now() not null,
    stdout text,
    stderr text
);

create table if not exists public.usage_meters (
    id bigint generated always as identity primary key,
    user_id uuid not null,
    sandbox_id uuid,
    billing_model text default 'payg' not null,
    bucket timestamptz not null,
    vcpu_seconds double precision default 0 not null,
    ram_gb_seconds double precision default 0 not null,
    disk_gb_seconds double precision default 0 not null,
    created_at timestamptz default now() not null
);

create unique index if not exists usage_meters_sandbox_bucket_idx
    on public.usage_meters (sandbox_id, bucket);

create schema if not exists ops;
revoke all on schema ops from public;

-- The singleton policy currently used by the control plane.
create or replace view ops.current_policy
with (security_invoker = true)
as
select
    id,
    rate_limit,
    rate_burst,
    default_exec_timeout_ms,
    max_exec_timeout_ms,
    workers,
    pool_size,
    max_sessions,
    session_idle_timeout_ms,
    session_max_lifetime_ms,
    max_api_keys,
    updated_at
from public.execution_policies
where id = 'default';

-- One row per billing model, using the most recent rate already in effect.
create or replace view ops.current_pricing
with (security_invoker = true)
as
select distinct on (billing_model)
    billing_model,
    version,
    effective_from,
    rate_vcpu_sec,
    rate_gb_ram_sec,
    rate_gb_disk_hour,
    rate_execution_sec
from public.pricing_rates
where effective_from <= now()
order by billing_model, effective_from desc, version desc;

-- High-level numbers for status cards or a single-row dashboard.
create or replace view ops.platform_overview
with (security_invoker = true)
as
select
    now() as observed_at,
    (select count(*) from public.profiles) as profiles,
    (select count(*) from public.api_keys where is_active) as active_api_keys,
    (select count(*) from public.profiles where balance_usd > 0) as funded_profiles,
    (select count(*) from public.profiles where balance_usd <= 0) as empty_profiles,
    (select coalesce(sum(balance_usd), 0) from public.profiles) as total_balance_usd,
    (select count(*) from public.sandboxes) as sandboxes_total,
    (select count(*) from public.sandboxes where state = 'active') as sandboxes_active,
    (select count(*) from public.sandboxes where state = 'paused') as sandboxes_paused,
    (select count(*) from public.sandboxes where state = 'destroyed') as sandboxes_destroyed,
    (
        select count(*)
        from public.sandboxes
        where state <> 'destroyed' and host_id is null
    ) as live_sandboxes_without_worker,
    (
        select count(*)
        from public.usage_logs
        where created_at >= date_trunc('day', now())
    ) as executions_today,
    (
        select coalesce(sum(cost_usd), 0)
        from public.usage_logs
        where created_at >= date_trunc('day', now())
    ) as recorded_cost_today_usd,
    (
        select coalesce(sum(cost_usd), 0)
        from public.usage_logs
        where created_at >= now() - interval '30 days'
    ) as recorded_cost_30d_usd,
    (select max(created_at) from public.usage_logs) as latest_execution_at,
    (select max(bucket) from public.usage_meters) as latest_meter_bucket,
    (
        select rate_execution_sec
        from ops.current_pricing
        where billing_model = 'payg'
    ) as current_payg_execution_rate_usd_per_second;

-- Use hour on the X-axis and executions or recorded_cost_usd on the Y-axis.
create or replace view ops.execution_usage_hourly
with (security_invoker = true)
as
select
    date_trunc('hour', created_at) as hour,
    execution_type,
    count(*) as executions,
    count(distinct user_id) as active_profiles,
    count(*) filter (where exit_code = 0) as successful_executions,
    count(*) filter (where exit_code <> 0) as failed_executions,
    sum(duration_ms) / 1000.0 as execution_seconds,
    coalesce(sum(cost_usd), 0) as recorded_cost_usd
from public.usage_logs
group by date_trunc('hour', created_at), execution_type;

-- Daily recorded execution billing. This is sourced from usage_logs only.
create or replace view ops.billing_daily
with (security_invoker = true)
as
select
    date_trunc('day', created_at)::date as day,
    count(*) as executions,
    count(distinct user_id) as active_profiles,
    sum(duration_ms) / 1000.0 as execution_seconds,
    coalesce(sum(cost_usd), 0) as recorded_cost_usd,
    avg(cost_usd) as average_cost_per_execution_usd
from public.usage_logs
group by date_trunc('day', created_at)::date;

-- Metered resource consumption with the price version effective at each bucket.
-- Keep this chart separate from billing_daily until the billing pipeline defines
-- whether execution cost and resource cost are additive.
create or replace view ops.resource_usage_hourly
with (security_invoker = true)
as
with hourly as (
    select
        date_trunc('hour', bucket) as hour,
        billing_model,
        sum(vcpu_seconds) as vcpu_seconds,
        sum(ram_gb_seconds) as ram_gb_seconds,
        sum(disk_gb_seconds) as disk_gb_seconds
    from public.usage_meters
    group by date_trunc('hour', bucket), billing_model
)
select
    h.hour,
    h.billing_model,
    h.vcpu_seconds,
    h.ram_gb_seconds,
    h.disk_gb_seconds,
    p.version as pricing_version,
    (
        h.vcpu_seconds * p.rate_vcpu_sec
        + h.ram_gb_seconds * p.rate_gb_ram_sec
        + (h.disk_gb_seconds / 3600.0) * p.rate_gb_disk_hour
    ) as estimated_resource_cost_usd
from hourly h
left join lateral (
    select
        version,
        rate_vcpu_sec,
        rate_gb_ram_sec,
        rate_gb_disk_hour
    from public.pricing_rates
    where billing_model = h.billing_model
      and effective_from <= h.hour
    order by effective_from desc, version desc
    limit 1
) p on true;

-- Inventory without sandbox IDs or host names.
create or replace view ops.sandbox_inventory
with (security_invoker = true)
as
select
    state,
    billing_model,
    case when host_id is null then 'unassigned' else 'assigned' end as worker_assignment,
    count(*) as sandboxes,
    sum(vcpus) as allocated_vcpus,
    sum(memory_mb) as allocated_memory_mb,
    sum(disk_gb) as allocated_disk_gb,
    min(created_at) as oldest_created_at,
    max(last_used_at) as latest_activity_at
from public.sandboxes
group by
    state,
    billing_model,
    case when host_id is null then 'unassigned' else 'assigned' end;

-- Cumulative PostgreSQL activity counters. These reset when PostgreSQL stats
-- are reset, so use the snapshot function below for time-based write rates.
create or replace view ops.table_activity
with (security_invoker = true)
as
select
    s.schemaname as schema_name,
    s.relname as table_name,
    s.seq_scan,
    s.idx_scan,
    s.n_tup_ins as rows_inserted,
    s.n_tup_upd as rows_updated,
    s.n_tup_del as rows_deleted,
    s.n_live_tup as estimated_live_rows,
    s.n_dead_tup as estimated_dead_rows,
    s.last_autovacuum,
    s.last_autoanalyze,
    d.stats_reset
from pg_catalog.pg_stat_user_tables s
join pg_catalog.pg_stat_database d
  on d.datname = current_database()
where s.schemaname = 'public'
  and s.relname in (
      'user',
      'session',
      'account',
      'verification',
      'profiles',
      'api_keys',
      'execution_policies',
      'pricing_rates',
      'sandboxes',
      'sandbox_runs',
      'sandbox_logs',
      'usage_logs',
      'usage_meters'
  );

-- Historical snapshots make insert/update/delete rates chartable. Schedule
-- `select ops.capture_table_activity();` every five minutes in Supabase Cron.
create table if not exists ops.table_activity_snapshots (
    captured_at timestamptz not null,
    stats_reset timestamptz,
    schema_name text not null,
    table_name text not null,
    rows_inserted bigint not null,
    rows_updated bigint not null,
    rows_deleted bigint not null,
    estimated_live_rows bigint not null,
    estimated_dead_rows bigint not null,
    seq_scan bigint not null,
    idx_scan bigint not null,
    primary key (captured_at, schema_name, table_name)
);

create index if not exists table_activity_snapshots_table_time_idx
    on ops.table_activity_snapshots (schema_name, table_name, captured_at desc);

create or replace function ops.capture_table_activity()
returns bigint
language plpgsql
security invoker
set search_path = pg_catalog, public, ops
as $function$
declare
    captured_rows bigint;
    snapshot_time timestamptz := clock_timestamp();
begin
    insert into ops.table_activity_snapshots (
        captured_at,
        stats_reset,
        schema_name,
        table_name,
        rows_inserted,
        rows_updated,
        rows_deleted,
        estimated_live_rows,
        estimated_dead_rows,
        seq_scan,
        idx_scan
    )
    select
        snapshot_time,
        stats_reset,
        schema_name,
        table_name,
        rows_inserted,
        rows_updated,
        rows_deleted,
        estimated_live_rows,
        estimated_dead_rows,
        seq_scan,
        idx_scan
    from ops.table_activity;

    get diagnostics captured_rows = row_count;
    return captured_rows;
end;
$function$;

-- Rates become available after at least two snapshots have been captured.
create or replace view ops.table_write_rates
with (security_invoker = true)
as
with previous as (
    select
        captured_at,
        stats_reset,
        schema_name,
        table_name,
        rows_inserted,
        rows_updated,
        rows_deleted,
        lag(captured_at) over activity_window as previous_captured_at,
        lag(rows_inserted) over activity_window as previous_rows_inserted,
        lag(rows_updated) over activity_window as previous_rows_updated,
        lag(rows_deleted) over activity_window as previous_rows_deleted
    from ops.table_activity_snapshots
    window activity_window as (
        partition by schema_name, table_name, stats_reset
        order by captured_at
    )
)
select
    captured_at,
    schema_name,
    table_name,
    extract(epoch from captured_at - previous_captured_at) as sample_seconds,
    greatest(rows_inserted - previous_rows_inserted, 0) as inserted_in_sample,
    greatest(rows_updated - previous_rows_updated, 0) as updated_in_sample,
    greatest(rows_deleted - previous_rows_deleted, 0) as deleted_in_sample,
    greatest(rows_inserted - previous_rows_inserted, 0)
        / nullif(extract(epoch from captured_at - previous_captured_at), 0)
        as inserts_per_second,
    greatest(rows_updated - previous_rows_updated, 0)
        / nullif(extract(epoch from captured_at - previous_captured_at), 0)
        as updates_per_second,
    greatest(rows_deleted - previous_rows_deleted, 0)
        / nullif(extract(epoch from captured_at - previous_captured_at), 0)
        as deletes_per_second
from previous
where previous_captured_at is not null;

-- Every check stays visible even when healthy (affected_rows = 0).
create or replace view ops.integrity_checks
with (security_invoker = true)
as
select
    'critical'::text as severity,
    'default_execution_policy_count'::text as check_name,
    case when count(*) = 1 then 0 else greatest(count(*), 1) end::bigint as affected_rows,
    'Exactly one execution_policies row with id=default is required.'::text as detail
from public.execution_policies
where id = 'default'

union all

select
    'critical',
    'active_payg_price_missing',
    case when count(*) > 0 then 0 else 1 end::bigint,
    'At least one PAYG price must have effective_from at or before now().'
from public.pricing_rates
where billing_model = 'payg' and effective_from <= now()

union all

select
    'critical',
    'live_sandbox_without_worker',
    count(*)::bigint,
    'A non-destroyed sandbox has no host_id and cannot be routed to its disk host.'
from public.sandboxes
where state <> 'destroyed' and host_id is null

union all

select
    'critical',
    'sandbox_profile_orphan',
    count(*)::bigint,
    'A sandbox references a profile that does not exist.'
from public.sandboxes s
left join public.profiles p on p.id = s.user_id
where p.id is null

union all

select
    'critical',
    'usage_profile_orphan',
    count(*)::bigint,
    'A usage log references a profile that does not exist.'
from public.usage_logs u
left join public.profiles p on p.id = u.user_id
where p.id is null

union all

select
    'warning',
    'sandbox_api_key_orphan',
    count(*)::bigint,
    'A sandbox references an API key that does not exist.'
from public.sandboxes s
left join public.api_keys k on k.id = s.api_key_id
where s.api_key_id is not null and k.id is null

union all

select
    'warning',
    'usage_api_key_orphan',
    count(*)::bigint,
    'A usage log references an API key that does not exist.'
from public.usage_logs u
left join public.api_keys k on k.id = u.api_key_id
where k.id is null

union all

select
    'critical',
    'negative_balance',
    count(*)::bigint,
    'A profile balance is below zero.'
from public.profiles
where balance_usd < 0

union all

select
    'warning',
    'sandbox_without_pricing_model',
    count(*)::bigint,
    'A sandbox billing_model has no pricing_rates row.'
from public.sandboxes s
where not exists (
    select 1
    from public.pricing_rates p
    where p.billing_model = s.billing_model
)

union all

select
    'warning',
    'active_sandbox_without_recent_meter',
    count(*)::bigint,
    'An active sandbox older than ten minutes has no usage meter in the last ten minutes.'
from public.sandboxes s
where s.state = 'active'
  and s.created_at < now() - interval '10 minutes'
  and not exists (
      select 1
      from public.usage_meters m
      where m.sandbox_id = s.id
        and m.bucket >= now() - interval '10 minutes'
  );

comment on schema ops is
    'Private aggregate operational reporting for Supabase Studio.';
comment on view ops.platform_overview is
    'Single-row platform, sandbox, metering, and PAYG summary.';
comment on view ops.execution_usage_hourly is
    'Hourly executions and recorded cost from usage_logs.';
comment on view ops.resource_usage_hourly is
    'Hourly metered resources and estimated resource price.';
comment on view ops.integrity_checks is
    'Production correctness checks; affected_rows=0 means healthy.';
comment on function ops.capture_table_activity() is
    'Snapshots cumulative PostgreSQL table statistics for rate charts.';

revoke all on all tables in schema ops from public;
revoke all on all functions in schema ops from public;

-- Supabase service_role may read these aggregates, but anon and authenticated
-- receive no access. security_invoker views still enforce base-table access.
do $grant$
begin
    if exists (select 1 from pg_catalog.pg_roles where rolname = 'service_role') then
        execute 'grant usage on schema ops to service_role';
        execute 'grant select on all tables in schema ops to service_role';
        execute 'grant execute on function ops.capture_table_activity() to service_role';
    end if;
end;
$grant$;

commit;

-- Capture the first sample after installation. Run this again after five
-- minutes, or schedule it in Supabase Cron, before querying table_write_rates.
select ops.capture_table_activity();

-- Suggested Supabase chart queries:
--
-- Status cards:
--   select * from ops.platform_overview;
--
-- Execution and recorded PAYG cost trend:
--   select * from ops.execution_usage_hourly
--   where hour >= now() - interval '30 days'
--   order by hour;
--
-- Daily recorded billing:
--   select * from ops.billing_daily order by day;
--
-- Sandbox state and worker assignment:
--   select * from ops.sandbox_inventory order by state, billing_model;
--
-- Correctness alerts:
--   select * from ops.integrity_checks
--   where affected_rows > 0
--   order by severity, affected_rows desc;
--
-- Table writes over time (requires at least two snapshots):
--   select * from ops.table_write_rates
--   where captured_at >= now() - interval '24 hours'
--   order by captured_at, table_name;
