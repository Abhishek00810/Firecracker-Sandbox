-- Convert the legacy free/pro tier schema to a single server-owned PAYG policy.
-- Apply this migration before deploying the backend that reads execution_policies.
begin;

do $$
begin
    if not exists (select 1 from public.tier_configs where name = 'payg') then
        raise exception 'tier_configs must contain a payg row before migration';
    end if;
    if exists (select 1 from public.tier_configs where name <> 'payg') then
        raise exception 'non-PAYG tier_configs rows still exist; reconcile them before migration';
    end if;
end
$$;

alter table public.tier_configs rename to execution_policies;
alter table public.execution_policies rename column name to id;

-- Remove runtime-policy foreign keys before changing the singleton key.
alter table public.profiles drop column tier;
alter table public.sandboxes drop constraint if exists sandboxes_tier_fkey;
alter table public.sandboxes rename column tier to billing_model;
alter table public.usage_meters drop constraint if exists usage_meters_tier_fkey;
alter table public.usage_meters rename column tier to billing_model;
alter table public.tier_rates drop constraint if exists tier_rates_tier_fkey;
alter table public.execution_policies drop constraint if exists tier_configs_name_check;

update public.execution_policies set id = 'default' where id = 'payg';
alter table public.execution_policies
    add constraint execution_policies_singleton check (id = 'default');

alter table public.tier_rates rename to pricing_rates;
alter table public.pricing_rates rename column tier to billing_model;
alter table public.pricing_rates
    add column rate_execution_sec double precision not null default 0;

update public.pricing_rates
set rate_execution_sec = (
    select rate_usd_per_sec::double precision
    from public.execution_policies
    where id = 'default'
)
where billing_model = 'payg';

insert into public.pricing_rates
    (billing_model, version, effective_from, rate_vcpu_sec, rate_gb_ram_sec,
     rate_gb_disk_hour, rate_execution_sec)
select 'payg', 1, now(), 0, 0, 0, rate_usd_per_sec::double precision
from public.execution_policies
where id = 'default'
  and not exists (
      select 1 from public.pricing_rates where billing_model = 'payg'
  );

alter table public.execution_policies drop column rate_usd_per_sec;

alter table public.profiles rename column free_usd_remaining to balance_usd;
alter table public.profiles rename column free_credit_granted to signup_credit_granted;

comment on table public.execution_policies is
    'Singleton runtime policy. API clients select resources, never plans.';
comment on table public.pricing_rates is
    'Versioned billing prices, independent from runtime scheduling and limits.';
comment on column public.profiles.balance_usd is
    'Available PAYG dollar balance, including promotional and purchased funds.';
comment on column public.sandboxes.billing_model is
    'Billing model captured when the sandbox was created; currently always payg.';

commit;
