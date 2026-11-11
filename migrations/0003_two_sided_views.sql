-- +goose Up
-- +goose StatementBegin

-- Binary conditions in which RN1 actually traded BOTH outcome tokens.
-- We restrict to TRADE rows so the token_id (asset) is populated.
create or replace view v_binary_conditions_traded as
select
    condition_id,
    min(asset) as token_a,
    max(asset) as token_b,
    count(distinct asset) as token_count
from account_actions
where action_type = 'TRADE'
  and asset is not null and asset <> ''
  and condition_id is not null and condition_id <> ''
group by condition_id
having count(distinct asset) = 2;

-- Stage A: per-condition aggregate of two-sided buying/selling activity.
-- Each row is one binary condition where RN1 has bought at least one of
-- the two outcome tokens. Side A / Side B are assigned deterministically
-- by lexicographic order of the token_id, so the labels are stable across
-- runs but carry no semantic meaning (which outcome wins is not inferred).
create or replace view v_two_sided_aggregate as
with buy_agg as (
    select
        a.condition_id,
        sk.token_a,
        sk.token_b,
        coalesce(sum(a.size)      filter (where a.asset = sk.token_a and lower(a.side) = 'buy'),  0) as buy_size_a,
        coalesce(sum(a.usdc_size) filter (where a.asset = sk.token_a and lower(a.side) = 'buy'),  0) as buy_usdc_a,
        coalesce(sum(a.size)      filter (where a.asset = sk.token_a and lower(a.side) = 'sell'), 0) as sell_size_a,
        coalesce(sum(a.size)      filter (where a.asset = sk.token_b and lower(a.side) = 'buy'),  0) as buy_size_b,
        coalesce(sum(a.usdc_size) filter (where a.asset = sk.token_b and lower(a.side) = 'buy'),  0) as buy_usdc_b,
        coalesce(sum(a.size)      filter (where a.asset = sk.token_b and lower(a.side) = 'sell'), 0) as sell_size_b,
        max(a.market_slug)       as market_slug,
        max(a.event_slug)        as event_slug,
        max(a.title)             as title,
        max(a.outcome) filter (where a.asset = sk.token_a) as outcome_a,
        max(a.outcome) filter (where a.asset = sk.token_b) as outcome_b,
        min(a.timestamp_utc) as first_trade_ts,
        max(a.timestamp_utc) as last_trade_ts
    from account_actions a
    join v_binary_conditions_traded sk on sk.condition_id = a.condition_id
    where a.action_type = 'TRADE'
    group by a.condition_id, sk.token_a, sk.token_b
)
select
    ba.condition_id,
    ba.token_a, ba.token_b,
    ba.outcome_a, ba.outcome_b,
    ba.market_slug, ba.event_slug, ba.title,
    ba.buy_size_a, ba.buy_usdc_a, ba.sell_size_a,
    ba.buy_size_b, ba.buy_usdc_b, ba.sell_size_b,
    case when ba.buy_size_a > 0 then ba.buy_usdc_a / ba.buy_size_a else null end as wavg_price_a,
    case when ba.buy_size_b > 0 then ba.buy_usdc_b / ba.buy_size_b else null end as wavg_price_b,
    (ba.buy_usdc_a + ba.buy_usdc_b)                                as total_cost_both_sides,
    -- Each share of the winning outcome resolves to 1 USDC.
    ba.buy_size_a                                                  as payout_if_a_wins,
    ba.buy_size_b                                                  as payout_if_b_wins,
    ba.buy_size_a - (ba.buy_usdc_a + ba.buy_usdc_b)                as net_if_a_wins,
    ba.buy_size_b - (ba.buy_usdc_a + ba.buy_usdc_b)                as net_if_b_wins,
    case
        when ba.buy_size_a > 0 and ba.buy_size_b > 0 then
            case
                when (ba.buy_size_a - (ba.buy_usdc_a + ba.buy_usdc_b)) > 0
                 and (ba.buy_size_b - (ba.buy_usdc_a + ba.buy_usdc_b)) > 0 then 'POSITIVE_ENVELOPE'
                when (ba.buy_size_a - (ba.buy_usdc_a + ba.buy_usdc_b)) > 0
                  or (ba.buy_size_b - (ba.buy_usdc_a + ba.buy_usdc_b)) > 0 then 'PARTIAL_HEDGE'
                else 'NEGATIVE_BOTH'
            end
        else 'NOT_TWO_SIDED'
    end as envelope_classification,
    ba.first_trade_ts,
    ba.last_trade_ts
from buy_agg ba;

-- Aggregate distribution of two-sided envelope classifications.
create or replace view v_two_sided_summary as
select
    envelope_classification,
    count(*)                          as conditions,
    sum(total_cost_both_sides)        as sum_total_cost,
    sum(buy_usdc_a + buy_usdc_b)      as sum_buy_usdc_both,
    avg(wavg_price_a + wavg_price_b)  as avg_sum_of_avg_prices
from v_two_sided_aggregate
group by envelope_classification
order by conditions desc;

-- Top positive-envelope cases by gross arbitrage margin.
-- margin = min(net_if_a_wins, net_if_b_wins). All-positive only.
create or replace view v_two_sided_positive_envelope_top as
select
    condition_id,
    market_slug,
    event_slug,
    title,
    outcome_a, outcome_b,
    buy_size_a, buy_size_b,
    wavg_price_a, wavg_price_b,
    total_cost_both_sides,
    net_if_a_wins,
    net_if_b_wins,
    least(net_if_a_wins, net_if_b_wins) as guaranteed_margin,
    first_trade_ts,
    last_trade_ts
from v_two_sided_aggregate
where envelope_classification = 'POSITIVE_ENVELOPE'
order by least(net_if_a_wins, net_if_b_wins) desc
limit 50;

-- Stage B: time-resolved two-sided overlap episodes.
-- For each binary condition, compute running shares on both sides across
-- TRADE and MERGE rows (MERGE reduces both sides by the same size).
-- An "overlap" timestamp is one where both running balances are strictly
-- positive. Adjacent overlap rows are grouped into episodes via the
-- gaps-and-islands pattern.
create or replace view v_two_sided_running as
with combined as (
    select
        a.condition_id,
        sk.token_a, sk.token_b,
        a.timestamp_utc,
        a.transaction_hash,
        a.action_type,
        a.asset,
        a.side,
        case
            when a.action_type = 'TRADE' and a.asset = sk.token_a and lower(a.side) = 'buy'  then  a.size
            when a.action_type = 'TRADE' and a.asset = sk.token_a and lower(a.side) = 'sell' then -a.size
            when a.action_type = 'MERGE'                                                     then -a.size
            else 0
        end as delta_a,
        case
            when a.action_type = 'TRADE' and a.asset = sk.token_b and lower(a.side) = 'buy'  then  a.size
            when a.action_type = 'TRADE' and a.asset = sk.token_b and lower(a.side) = 'sell' then -a.size
            when a.action_type = 'MERGE'                                                     then -a.size
            else 0
        end as delta_b,
        case
            when a.action_type = 'TRADE' and a.asset = sk.token_a and lower(a.side) = 'buy' then a.usdc_size else 0
        end as buy_usdc_a_step,
        case
            when a.action_type = 'TRADE' and a.asset = sk.token_b and lower(a.side) = 'buy' then a.usdc_size else 0
        end as buy_usdc_b_step,
        case
            when a.action_type = 'TRADE' and a.asset = sk.token_a and lower(a.side) = 'buy' then a.size else 0
        end as buy_size_a_step,
        case
            when a.action_type = 'TRADE' and a.asset = sk.token_b and lower(a.side) = 'buy' then a.size else 0
        end as buy_size_b_step
    from account_actions a
    join v_binary_conditions_traded sk on sk.condition_id = a.condition_id
    where a.action_type in ('TRADE','MERGE')
)
select
    condition_id, timestamp_utc, transaction_hash, action_type, side, asset,
    token_a, token_b,
    sum(delta_a)          over w as shares_a,
    sum(delta_b)          over w as shares_b,
    sum(buy_size_a_step)  over w as cum_buy_size_a,
    sum(buy_size_b_step)  over w as cum_buy_size_b,
    sum(buy_usdc_a_step)  over w as cum_buy_usdc_a,
    sum(buy_usdc_b_step)  over w as cum_buy_usdc_b
from combined
window w as (
    partition by condition_id
    order by timestamp_utc, transaction_hash, asset
    rows between unbounded preceding and current row
);

-- Tag each running row with a "both held" flag and assign an episode id
-- using gaps-and-islands. An episode is a maximal run of consecutive
-- "both held" rows within one condition_id.
create or replace view v_two_sided_overlap_episodes as
with flagged as (
    select
        condition_id, timestamp_utc, transaction_hash, action_type, asset,
        token_a, token_b,
        shares_a, shares_b,
        cum_buy_size_a, cum_buy_size_b, cum_buy_usdc_a, cum_buy_usdc_b,
        (shares_a > 1e-9 and shares_b > 1e-9) as both_held
    from v_two_sided_running
),
transitions as (
    select
        *,
        case when both_held
             and coalesce(lag(both_held) over (partition by condition_id order by timestamp_utc, transaction_hash, asset), false) = false
             then 1 else 0
        end as starts_episode
    from flagged
),
ep_keys as (
    select
        *,
        sum(starts_episode) over (
            partition by condition_id
            order by timestamp_utc, transaction_hash, asset
            rows between unbounded preceding and current row
        ) as episode_id
    from transitions
)
select
    condition_id,
    episode_id,
    min(token_a) as token_a,
    min(token_b) as token_b,
    min(timestamp_utc) as overlap_start_ts,
    max(timestamp_utc) as overlap_end_ts,
    extract(epoch from (max(timestamp_utc) - min(timestamp_utc)))::bigint as overlap_seconds,
    count(*) as overlap_rows,
    max(shares_a) as peak_shares_a,
    max(shares_b) as peak_shares_b,
    max(cum_buy_size_a) as cum_buy_size_a_at_peak,
    max(cum_buy_size_b) as cum_buy_size_b_at_peak,
    max(cum_buy_usdc_a) as cum_buy_usdc_a_at_peak,
    max(cum_buy_usdc_b) as cum_buy_usdc_b_at_peak
from ep_keys
where both_held
group by condition_id, episode_id;

-- Distribution / aggregate summaries for two-sided overlap episodes.
create or replace view v_two_sided_overlap_episode_summary as
select
    count(*)                                                    as overlap_episodes,
    count(distinct condition_id)                                as conditions_with_overlap,
    avg(overlap_seconds)                                        as avg_overlap_seconds,
    percentile_cont(0.5) within group (order by overlap_seconds) as median_overlap_seconds,
    max(overlap_seconds)                                        as max_overlap_seconds,
    sum(cum_buy_usdc_a_at_peak + cum_buy_usdc_b_at_peak)        as sum_cum_two_sided_usdc_at_peak
from v_two_sided_overlap_episodes;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

drop view if exists v_two_sided_overlap_episode_summary;
drop view if exists v_two_sided_overlap_episodes;
drop view if exists v_two_sided_running;
drop view if exists v_two_sided_positive_envelope_top;
drop view if exists v_two_sided_summary;
drop view if exists v_two_sided_aggregate;
drop view if exists v_binary_conditions_traded;

-- +goose StatementEnd
