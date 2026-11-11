-- +goose Up
-- +goose StatementBegin

-- Per-event-slug aggregation of every action RN1 took in that event,
-- combining TRADE/MERGE/REDEEM rows. This is the event-level analogue
-- of the per-condition lifecycle from Layer 1.
create or replace view v_event_clusters as
select
    event_slug,
    count(*)                                                                         as total_actions,
    count(distinct condition_id) filter (where condition_id is not null
                                          and condition_id <> '')                    as distinct_markets,
    count(distinct asset)        filter (where asset is not null
                                          and asset <> '')                           as distinct_tokens,
    count(*) filter (where action_type = 'TRADE' and lower(side) = 'buy')            as trade_buys,
    count(*) filter (where action_type = 'TRADE' and lower(side) = 'sell')           as trade_sells,
    count(*) filter (where action_type = 'MERGE')                                    as merges,
    count(*) filter (where action_type = 'REDEEM')                                   as redeems,
    min(timestamp_utc)                                                               as first_action_ts,
    max(timestamp_utc)                                                               as last_action_ts,
    extract(epoch from (max(timestamp_utc) - min(timestamp_utc)))::bigint            as cluster_span_seconds,
    coalesce(sum(usdc_size) filter (where action_type = 'TRADE' and lower(side) = 'buy'),  0) as total_buy_usdc,
    coalesce(sum(usdc_size) filter (where action_type = 'TRADE' and lower(side) = 'sell'), 0) as total_sell_usdc,
    coalesce(sum(usdc_size) filter (where action_type = 'MERGE'),  0)                as total_merge_usdc,
    coalesce(sum(usdc_size) filter (where action_type = 'REDEEM'), 0)                as total_redeem_usdc,
    max(title)        as sample_title
from account_actions
where event_slug is not null and event_slug <> ''
group by event_slug;

-- For each event slug, count how many of the embedded markets carried
-- two-sided activity, broken down by envelope classification from
-- Layer 2.
create or replace view v_event_two_sided_count as
select
    e.event_slug,
    count(*)                                                                       as two_sided_markets,
    count(*) filter (where t.envelope_classification = 'POSITIVE_ENVELOPE')        as positive_envelope_markets,
    count(*) filter (where t.envelope_classification = 'PARTIAL_HEDGE')            as partial_hedge_markets,
    count(*) filter (where t.envelope_classification = 'NEGATIVE_BOTH')            as negative_both_markets
from v_two_sided_aggregate t
join (
    select distinct event_slug, condition_id
    from account_actions
    where event_slug is not null and event_slug <> ''
      and condition_id is not null and condition_id <> ''
) e on e.condition_id = t.condition_id
group by e.event_slug;

-- Full event-cluster summary with bundle-size class and exit class.
-- bundle_size_class:
--   SINGLE_MARKET   1 distinct market
--   SMALL_BUNDLE    2..3
--   MID_BUNDLE      4..6
--   LARGE_BUNDLE    7+
--
-- cluster_exit_class:
--   SETTLEMENT_ONLY  no sells, at least one MERGE or REDEEM
--   HAS_SELL         at least one TRADE SELL action in the cluster
--   ALL_OPEN         no sells and no MERGE / REDEEM yet
--   INDETERMINATE    fallback (no sells, no settlement, but other types)
create or replace view v_event_cluster_summary as
select
    c.event_slug,
    c.distinct_markets,
    c.distinct_tokens,
    c.total_actions,
    c.trade_buys, c.trade_sells,
    c.merges, c.redeems,
    c.total_buy_usdc, c.total_sell_usdc,
    c.total_merge_usdc, c.total_redeem_usdc,
    c.first_action_ts, c.last_action_ts, c.cluster_span_seconds,
    c.sample_title,
    coalesce(ts.two_sided_markets, 0)         as two_sided_markets,
    coalesce(ts.positive_envelope_markets, 0) as positive_envelope_markets,
    coalesce(ts.partial_hedge_markets, 0)     as partial_hedge_markets,
    coalesce(ts.negative_both_markets, 0)     as negative_both_markets,
    case
        when c.distinct_markets = 1                 then 'SINGLE_MARKET'
        when c.distinct_markets between 2 and 3     then 'SMALL_BUNDLE'
        when c.distinct_markets between 4 and 6     then 'MID_BUNDLE'
        else                                              'LARGE_BUNDLE'
    end                                       as bundle_size_class,
    case
        when c.trade_sells > 0                                       then 'HAS_SELL'
        when c.trade_sells = 0 and (c.merges > 0 or c.redeems > 0)   then 'SETTLEMENT_ONLY'
        when c.trade_sells = 0 and c.merges = 0 and c.redeems = 0    then 'ALL_OPEN'
        else                                                              'INDETERMINATE'
    end                                       as cluster_exit_class
from v_event_clusters c
left join v_event_two_sided_count ts on ts.event_slug = c.event_slug;

-- Distribution of bundles by size class and exit class.
create or replace view v_event_cluster_distribution as
select
    bundle_size_class,
    cluster_exit_class,
    count(*)                          as clusters,
    sum(distinct_markets)             as sum_markets,
    sum(total_buy_usdc)               as sum_buy_usdc,
    sum(two_sided_markets)            as sum_two_sided_markets,
    sum(positive_envelope_markets)    as sum_positive_envelope_markets
from v_event_cluster_summary
group by bundle_size_class, cluster_exit_class
order by bundle_size_class, cluster_exit_class;

-- Aggregate by bundle size only.
create or replace view v_event_cluster_size_distribution as
select
    bundle_size_class,
    count(*)                          as clusters,
    sum(distinct_markets)             as sum_markets,
    sum(total_buy_usdc)               as sum_buy_usdc,
    avg(distinct_markets)::numeric(10,2) as avg_markets_per_cluster,
    sum(two_sided_markets)            as sum_two_sided_markets,
    sum(positive_envelope_markets)    as sum_positive_envelope_markets
from v_event_cluster_summary
group by bundle_size_class
order by avg(distinct_markets) desc;

-- Per-leg (per condition_id within an event) detail with entry ordering.
-- This is the row-level table used to reconstruct how RN1 built a
-- specific event cluster over time.
create or replace view v_event_market_legs as
with leg_agg as (
    select
        event_slug,
        condition_id,
        max(market_slug) as market_slug,
        max(title) as title,
        count(*) filter (where action_type = 'TRADE' and lower(side) = 'buy')  as trade_buys,
        count(*) filter (where action_type = 'TRADE' and lower(side) = 'sell') as trade_sells,
        count(*) filter (where action_type = 'MERGE')                          as merges,
        count(*) filter (where action_type = 'REDEEM')                         as redeems,
        count(distinct asset) filter (where asset is not null and asset <> '') as distinct_tokens_in_leg,
        coalesce(sum(usdc_size) filter (where action_type = 'TRADE' and lower(side) = 'buy'),  0) as buy_usdc,
        coalesce(sum(usdc_size) filter (where action_type = 'TRADE' and lower(side) = 'sell'), 0) as sell_usdc,
        min(timestamp_utc) as first_action_ts,
        max(timestamp_utc) as last_action_ts
    from account_actions
    where event_slug is not null and event_slug <> ''
      and condition_id is not null and condition_id <> ''
    group by event_slug, condition_id
)
select
    l.*,
    dense_rank() over (partition by l.event_slug order by l.first_action_ts) as entry_order,
    t.envelope_classification,
    t.wavg_price_a, t.wavg_price_b,
    t.net_if_a_wins, t.net_if_b_wins
from leg_agg l
left join v_two_sided_aggregate t on t.condition_id = l.condition_id;

-- Top event clusters by buy USDC where the cluster spans at least 2 markets.
create or replace view v_event_cluster_top_bundles as
select
    event_slug,
    sample_title,
    distinct_markets,
    two_sided_markets,
    positive_envelope_markets,
    trade_buys, trade_sells, merges, redeems,
    total_buy_usdc::numeric(20,2)   as total_buy_usdc,
    total_merge_usdc::numeric(20,2) as total_merge_usdc,
    total_redeem_usdc::numeric(20,2) as total_redeem_usdc,
    bundle_size_class,
    cluster_exit_class,
    first_action_ts, last_action_ts
from v_event_cluster_summary
where distinct_markets >= 2
order by total_buy_usdc desc
limit 50;

-- Recent active event clusters (last 3 days), for the "yesterday / most
-- recent active day" forensic appendix.
create or replace view v_event_cluster_recent_active as
select
    event_slug,
    sample_title,
    distinct_markets,
    two_sided_markets,
    positive_envelope_markets,
    trade_buys, trade_sells, merges, redeems,
    total_buy_usdc::numeric(20,2) as total_buy_usdc,
    bundle_size_class,
    cluster_exit_class,
    first_action_ts, last_action_ts
from v_event_cluster_summary
where last_action_ts >= (now() at time zone 'UTC' - interval '3 days')
order by last_action_ts desc, total_buy_usdc desc
limit 100;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

drop view if exists v_event_cluster_recent_active;
drop view if exists v_event_cluster_top_bundles;
drop view if exists v_event_market_legs;
drop view if exists v_event_cluster_size_distribution;
drop view if exists v_event_cluster_distribution;
drop view if exists v_event_cluster_summary;
drop view if exists v_event_two_sided_count;
drop view if exists v_event_clusters;

-- +goose StatementEnd
