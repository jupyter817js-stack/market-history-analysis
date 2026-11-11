-- +goose Up
-- +goose StatementBegin

-- May 2026 (UTC) action summary by action_type and side.
create or replace view v_may_2026_action_summary as
select
    action_type,
    side,
    count(*)                       as actions,
    count(distinct condition_id)   as distinct_markets,
    count(distinct event_slug)     as distinct_events,
    coalesce(sum(usdc_size), 0)    as total_usdc
from account_actions
where timestamp_utc >= '2026-05-01' and timestamp_utc < '2026-06-01'
group by action_type, side
order by action_type, side nulls first;

-- Pre-May vs May headline metrics, per-month bucket on UTC time.
create or replace view v_monthly_headline_metrics as
select
    date_trunc('month', timestamp_utc)                                                 as month_utc,
    count(*)                                                                           as actions,
    count(*) filter (where action_type = 'TRADE' and lower(side) = 'buy')              as buys,
    count(*) filter (where action_type = 'TRADE' and lower(side) = 'sell')             as sells,
    count(*) filter (where action_type = 'MERGE')                                      as merges,
    count(*) filter (where action_type = 'REDEEM')                                     as redeems,
    count(*) filter (where action_type = 'MAKER_REBATE')                               as rebates,
    count(distinct condition_id)                                                       as distinct_markets,
    count(distinct event_slug)                                                         as distinct_events,
    coalesce(sum(usdc_size) filter (where action_type = 'TRADE' and lower(side) = 'buy'),  0) as buy_usdc,
    coalesce(sum(usdc_size) filter (where action_type = 'TRADE' and lower(side) = 'sell'), 0) as sell_usdc,
    coalesce(sum(usdc_size) filter (where action_type = 'MERGE'),  0)                  as merge_usdc,
    coalesce(sum(usdc_size) filter (where action_type = 'REDEEM'), 0)                  as redeem_usdc
from account_actions
where timestamp_utc is not null
group by 1
order by 1;

-- May 2026 event clusters that have ANY May activity.
-- These are clusters whose action window touches May at least once.
create or replace view v_may_2026_event_clusters as
with may_events as (
    select distinct event_slug
    from account_actions
    where timestamp_utc >= '2026-05-01' and timestamp_utc < '2026-06-01'
      and event_slug is not null and event_slug <> ''
)
select c.*
from v_event_cluster_summary c
join may_events e on e.event_slug = c.event_slug;

-- May bundle-size distribution.
create or replace view v_may_2026_bundle_distribution as
select
    bundle_size_class,
    cluster_exit_class,
    count(*)                                  as clusters,
    sum(total_buy_usdc)                       as sum_buy_usdc,
    sum(two_sided_markets)                    as sum_two_sided_markets,
    sum(positive_envelope_markets)            as sum_positive_envelope_markets
from v_may_2026_event_clusters
group by bundle_size_class, cluster_exit_class
order by bundle_size_class, cluster_exit_class;

-- May aggregate at size-class only.
create or replace view v_may_2026_bundle_size_distribution as
select
    bundle_size_class,
    count(*)                              as clusters,
    sum(total_buy_usdc)::numeric(20,2)    as sum_buy_usdc,
    avg(distinct_markets)::numeric(10,2)  as avg_markets_per_cluster,
    sum(two_sided_markets)                as sum_two_sided_markets,
    sum(positive_envelope_markets)        as sum_positive_envelope_markets
from v_may_2026_event_clusters
group by bundle_size_class
order by avg(distinct_markets) desc;

-- Top May bundles by buy USDC.
create or replace view v_may_2026_top_bundles as
select
    event_slug,
    sample_title,
    distinct_markets,
    two_sided_markets,
    positive_envelope_markets,
    trade_buys, trade_sells, merges, redeems,
    total_buy_usdc::numeric(20,2)    as total_buy_usdc,
    total_merge_usdc::numeric(20,2)  as total_merge_usdc,
    total_redeem_usdc::numeric(20,2) as total_redeem_usdc,
    bundle_size_class,
    cluster_exit_class,
    first_action_ts, last_action_ts
from v_may_2026_event_clusters
where distinct_markets >= 2
order by total_buy_usdc desc
limit 50;

-- Latest active KST day = the most recent date_kst with any activity.
-- Reuse the existing v_latest_active_kst_day for the (date, count) pair.
create or replace view v_latest_kst_day_event_clusters as
with d as (
    select date_kst as d from v_latest_active_kst_day
),
events_active_today as (
    select distinct a.event_slug
    from account_actions a, d
    where a.date_kst = d.d
      and a.event_slug is not null
      and a.event_slug <> ''
)
select c.*
from v_event_cluster_summary c
join events_active_today e on e.event_slug = c.event_slug
order by c.total_buy_usdc desc;

-- Per-leg detail for latest-KST-day clusters.
create or replace view v_latest_kst_day_legs as
with events_active_today as (
    select distinct event_slug
    from account_actions
    where date_kst = (select date_kst from v_latest_active_kst_day)
      and event_slug is not null and event_slug <> ''
)
select l.*
from v_event_market_legs l
join events_active_today e on e.event_slug = l.event_slug
order by l.event_slug, l.entry_order;

-- Per-action detail for the latest KST day (for the forensic appendix).
create or replace view v_latest_kst_day_actions_detail as
select
    a.event_slug,
    a.condition_id,
    a.asset                as token_id,
    a.market_slug, a.title, a.outcome,
    a.action_type, a.side,
    a.price, a.size, a.usdc_size,
    a.timestamp_utc, a.timestamp_kst,
    a.transaction_hash
from account_actions a
where a.date_kst = (select date_kst from v_latest_active_kst_day)
  and a.event_slug is not null
  and a.event_slug <> ''
order by a.event_slug, a.timestamp_utc;

-- Top 5 latest-KST-day event clusters by buy USDC (the "headline" set
-- used in the forensic appendix).
create or replace view v_latest_kst_day_top5 as
select * from v_latest_kst_day_event_clusters limit 5;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

drop view if exists v_latest_kst_day_top5;
drop view if exists v_latest_kst_day_actions_detail;
drop view if exists v_latest_kst_day_legs;
drop view if exists v_latest_kst_day_event_clusters;
drop view if exists v_may_2026_top_bundles;
drop view if exists v_may_2026_bundle_size_distribution;
drop view if exists v_may_2026_bundle_distribution;
drop view if exists v_may_2026_event_clusters;
drop view if exists v_monthly_headline_metrics;
drop view if exists v_may_2026_action_summary;

-- +goose StatementEnd
