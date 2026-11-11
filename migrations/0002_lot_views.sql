-- +goose Up
-- +goose StatementBegin

-- Per (condition_id, token_id) trade-only roll-up.
-- TRADE rows expose asset(token_id); MERGE/REDEEM rows do NOT carry an
-- asset, so they roll up at the condition level only (see next view).
create or replace view v_market_lot_episodes as
with trades as (
    select
        account_address,
        proxy_wallet,
        condition_id,
        asset as token_id,
        outcome,
        outcome_index,
        market_slug,
        event_slug,
        title,
        side,
        size,
        usdc_size,
        price,
        timestamp_utc
    from account_actions
    where action_type = 'TRADE'
      and asset is not null and asset <> ''
      and condition_id is not null and condition_id <> ''
)
select
    account_address,
    proxy_wallet,
    condition_id,
    token_id,
    max(outcome)                                  as outcome,
    max(outcome_index)                            as outcome_index,
    max(market_slug)                              as market_slug,
    max(event_slug)                               as event_slug,
    max(title)                                    as title,
    min(timestamp_utc)                            as first_action_ts,
    max(timestamp_utc)                            as last_action_ts,
    count(*) filter (where lower(side)='buy')     as buys_count,
    count(*) filter (where lower(side)='sell')    as sells_count,
    coalesce(sum(size)      filter (where lower(side)='buy'),  0) as total_buy_shares,
    coalesce(sum(size)      filter (where lower(side)='sell'), 0) as total_sell_shares,
    coalesce(sum(usdc_size) filter (where lower(side)='buy'),  0) as total_buy_usdc,
    coalesce(sum(usdc_size) filter (where lower(side)='sell'), 0) as total_sell_usdc,
    case
      when sum(size) filter (where lower(side)='buy') > 0 then
           sum(usdc_size) filter (where lower(side)='buy') /
           sum(size)      filter (where lower(side)='buy')
      else null
    end                                           as wavg_buy_price,
    case
      when sum(size) filter (where lower(side)='sell') > 0 then
           sum(usdc_size) filter (where lower(side)='sell') /
           sum(size)      filter (where lower(side)='sell')
      else null
    end                                           as wavg_sell_price,
    coalesce(sum(size) filter (where lower(side)='buy'),  0) -
    coalesce(sum(size) filter (where lower(side)='sell'), 0)    as net_shares_excl_settlement
from trades
group by account_address, proxy_wallet, condition_id, token_id;

-- Per condition_id MERGE/REDEEM roll-up. MERGE size is the number of
-- full YES/NO pairs collapsed back to collateral; REDEEM size is the
-- USDC received at resolution.
create or replace view v_market_settlement_totals as
select
    account_address,
    proxy_wallet,
    condition_id,
    coalesce(sum(size) filter (where action_type='MERGE'),  0)      as merge_units,
    coalesce(sum(size) filter (where action_type='REDEEM'), 0)      as redeem_usdc,
    count(*) filter (where action_type='MERGE')                     as merge_events,
    count(*) filter (where action_type='REDEEM')                    as redeem_events,
    max(timestamp_utc) filter (where action_type='MERGE')           as last_merge_ts,
    max(timestamp_utc) filter (where action_type='REDEEM')          as last_redeem_ts
from account_actions
where condition_id is not null and condition_id <> ''
  and action_type in ('MERGE','REDEEM')
group by account_address, proxy_wallet, condition_id;

-- Token-level lifecycle summary with exit-path classification.
-- exit_path semantics:
--   OPEN              -- still holding shares net of trades, no settlement
--   SELL_ONLY         -- exit through market sells only
--   MERGE_ONLY        -- exit through MERGE only (full pair collapses)
--   REDEEM_ONLY       -- exit at market resolution only
--   MERGE_AND_REDEEM  -- combined settlement-side exit
--   MIXED             -- both market sells and settlement actions seen
--   INDETERMINATE     -- net shares = 0 with no exit signal recovered
create or replace view v_market_lot_summary as
select
    l.account_address,
    l.proxy_wallet,
    l.condition_id,
    l.token_id,
    l.market_slug,
    l.event_slug,
    l.title,
    l.outcome,
    l.outcome_index,
    l.first_action_ts,
    l.last_action_ts,
    l.buys_count,
    l.sells_count,
    l.total_buy_shares,
    l.total_sell_shares,
    l.total_buy_usdc,
    l.total_sell_usdc,
    l.wavg_buy_price,
    l.wavg_sell_price,
    l.net_shares_excl_settlement,
    coalesce(s.merge_units,   0)  as merge_units_condition,
    coalesce(s.redeem_usdc,   0)  as redeem_usdc_condition,
    coalesce(s.merge_events,  0)  as merge_events,
    coalesce(s.redeem_events, 0)  as redeem_events,
    s.last_merge_ts,
    s.last_redeem_ts,
    case
      when l.net_shares_excl_settlement > 0
           and coalesce(s.merge_units, 0) = 0
           and coalesce(s.redeem_usdc, 0) = 0 then 'OPEN'
      when l.sells_count > 0
           and coalesce(s.merge_units, 0) = 0
           and coalesce(s.redeem_usdc, 0) = 0 then 'SELL_ONLY'
      when l.sells_count = 0
           and coalesce(s.merge_units, 0) > 0
           and coalesce(s.redeem_usdc, 0) = 0 then 'MERGE_ONLY'
      when l.sells_count = 0
           and coalesce(s.merge_units, 0) = 0
           and coalesce(s.redeem_usdc, 0) > 0 then 'REDEEM_ONLY'
      when l.sells_count = 0
           and coalesce(s.merge_units, 0) > 0
           and coalesce(s.redeem_usdc, 0) > 0 then 'MERGE_AND_REDEEM'
      when l.sells_count > 0
           and (coalesce(s.merge_units, 0) > 0 or coalesce(s.redeem_usdc, 0) > 0) then 'MIXED'
      else 'INDETERMINATE'
    end                            as exit_path
from v_market_lot_episodes l
left join v_market_settlement_totals s
  on  s.account_address = l.account_address
  and (s.proxy_wallet is not distinct from l.proxy_wallet)
  and s.condition_id   = l.condition_id;

-- Quick aggregates over the lot summary for sanity-check / reporting.
create or replace view v_lot_summary_by_exit_path as
select
    exit_path,
    count(*) as token_episodes,
    count(distinct condition_id) as distinct_conditions,
    count(distinct event_slug)   as distinct_events,
    sum(total_buy_shares)        as sum_buy_shares,
    sum(total_buy_usdc)          as sum_buy_usdc
from v_market_lot_summary
group by exit_path
order by token_episodes desc;

create or replace view v_lot_summary_open_top_value as
select
    condition_id,
    token_id,
    market_slug,
    event_slug,
    title,
    outcome,
    net_shares_excl_settlement as open_shares,
    wavg_buy_price,
    total_buy_usdc,
    first_action_ts,
    last_action_ts
from v_market_lot_summary
where exit_path = 'OPEN'
order by total_buy_usdc desc
limit 50;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

drop view if exists v_lot_summary_open_top_value;
drop view if exists v_lot_summary_by_exit_path;
drop view if exists v_market_lot_summary;
drop view if exists v_market_settlement_totals;
drop view if exists v_market_lot_episodes;

-- +goose StatementEnd
