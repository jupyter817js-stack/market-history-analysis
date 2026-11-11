-- +goose Up
-- +goose StatementBegin

-- v_two_sided_envelope_per_fill extends v_two_sided_running with running
-- weighted-average prices on each side and an "envelope at this moment"
-- assessment. This lets us answer "was the positive envelope present at
-- the time both sides were held?" instead of only at the aggregate.
create or replace view v_two_sided_envelope_per_fill as
select
    r.*,
    case when r.cum_buy_size_a > 0 then r.cum_buy_usdc_a / r.cum_buy_size_a else null end as wavg_a_so_far,
    case when r.cum_buy_size_b > 0 then r.cum_buy_usdc_b / r.cum_buy_size_b else null end as wavg_b_so_far,
    case when r.cum_buy_size_a > 0 and r.cum_buy_size_b > 0
         then (r.cum_buy_usdc_a / r.cum_buy_size_a) + (r.cum_buy_usdc_b / r.cum_buy_size_b)
         else null end                                                          as sum_wavg_at_t,
    (r.shares_a > 1e-9 and r.shares_b > 1e-9)                                   as both_held_at_t,
    case when r.cum_buy_size_a > 0
              and r.cum_buy_size_b > 0
              and ((r.cum_buy_usdc_a / r.cum_buy_size_a) + (r.cum_buy_usdc_b / r.cum_buy_size_b)) < 1.0
              and r.shares_a > 1e-9
              and r.shares_b > 1e-9
         then true else false end                                               as positive_envelope_at_t
from v_two_sided_running r;

-- Per-condition realized settlement income from MERGE + REDEEM rows.
-- MERGE size collapses one full YES/NO pair into 1 USDC, so merge_size_total
-- doubles as the USDC inflow from merging. REDEEM size is the USDC received
-- at resolution on the winning side.
create or replace view v_condition_settlement_income as
select
    condition_id,
    coalesce(sum(size)      filter (where action_type='MERGE'),  0) as merge_size_total,
    coalesce(sum(size)      filter (where action_type='REDEEM'), 0) as redeem_size_total,
    coalesce(sum(usdc_size) filter (where action_type='MERGE'),  0) as merge_usdc_total,
    coalesce(sum(usdc_size) filter (where action_type='REDEEM'), 0) as redeem_usdc_total,
    count(*) filter (where action_type='MERGE')                     as merge_events,
    count(*) filter (where action_type='REDEEM')                    as redeem_events,
    min(timestamp_utc) filter (where action_type='MERGE')           as first_merge_ts,
    max(timestamp_utc) filter (where action_type='MERGE')           as last_merge_ts,
    min(timestamp_utc) filter (where action_type='REDEEM')          as first_redeem_ts,
    max(timestamp_utc) filter (where action_type='REDEEM')          as last_redeem_ts
from account_actions
where condition_id is not null and condition_id <> ''
  and action_type in ('MERGE','REDEEM')
group by condition_id;

-- Per-condition realized PnL estimate (settlement-inflow minus net buy
-- cost). For binary markets without SELL activity this approximates true
-- realized PnL; for conditions with SELL rows it folds sell proceeds into
-- "net buy cost". This is a best-effort estimate; it does not account for
-- intra-cluster carry between related markets.
create or replace view v_condition_realized_pnl_estimate as
with trade_net as (
    select
        condition_id,
        coalesce(sum(usdc_size) filter (where action_type='TRADE' and lower(side)='buy'),  0) as total_buy_usdc,
        coalesce(sum(usdc_size) filter (where action_type='TRADE' and lower(side)='sell'), 0) as total_sell_usdc
    from account_actions
    where action_type = 'TRADE'
      and condition_id is not null and condition_id <> ''
    group by condition_id
)
select
    t.condition_id,
    t.total_buy_usdc,
    t.total_sell_usdc,
    coalesce(s.merge_usdc_total,  0) as merge_usdc_total,
    coalesce(s.redeem_usdc_total, 0) as redeem_usdc_total,
    (t.total_sell_usdc
     + coalesce(s.merge_usdc_total,  0)
     + coalesce(s.redeem_usdc_total, 0)) as gross_inflow,
    (t.total_sell_usdc
     + coalesce(s.merge_usdc_total,  0)
     + coalesce(s.redeem_usdc_total, 0)
     - t.total_buy_usdc) as realized_pnl_estimate
from trade_net t
left join v_condition_settlement_income s on s.condition_id = t.condition_id;

-- Per-event realized PnL estimate (sum over the event's conditions).
create or replace view v_event_realized_pnl_estimate as
with leg_pnl as (
    select
        a.event_slug,
        p.condition_id,
        p.total_buy_usdc,
        p.total_sell_usdc,
        p.merge_usdc_total,
        p.redeem_usdc_total,
        p.realized_pnl_estimate
    from v_condition_realized_pnl_estimate p
    join (
        select distinct event_slug, condition_id
        from account_actions
        where event_slug is not null and event_slug <> ''
          and condition_id is not null and condition_id <> ''
    ) a on a.condition_id = p.condition_id
)
select
    event_slug,
    count(*)                              as legs,
    sum(total_buy_usdc)                   as total_buy_usdc,
    sum(total_sell_usdc)                  as total_sell_usdc,
    sum(merge_usdc_total)                 as merge_usdc_total,
    sum(redeem_usdc_total)                as redeem_usdc_total,
    sum(realized_pnl_estimate)            as realized_pnl_estimate
from leg_pnl
group by event_slug;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

drop view if exists v_event_realized_pnl_estimate;
drop view if exists v_condition_realized_pnl_estimate;
drop view if exists v_condition_settlement_income;
drop view if exists v_two_sided_envelope_per_fill;

-- +goose StatementEnd
