-- +goose Up
-- +goose StatementBegin

create table if not exists source_runs (
    id                  bigserial primary key,
    source_name         text not null,
    run_type            text not null,
    account_address     text not null,
    proxy_wallet        text null,
    started_at          timestamptz not null default now(),
    finished_at         timestamptz null,
    status              text not null,
    error_message       text null,
    request_count       integer not null default 0,
    raw_response_count  integer not null default 0,
    normalized_count    integer not null default 0,
    created_at          timestamptz not null default now()
);

create table if not exists raw_api_responses (
    id                  bigserial primary key,
    source_run_id       bigint not null references source_runs(id),
    source_name         text not null,
    endpoint            text not null,
    request_url         text not null,
    request_params      jsonb not null default '{}'::jsonb,
    response_body       jsonb not null,
    response_hash       text not null,
    http_status         integer not null,
    fetched_at          timestamptz not null,
    page_offset         integer null,
    cursor              text null,
    time_window_start   timestamptz null,
    time_window_end     timestamptz null,
    created_at          timestamptz not null default now()
);

create table if not exists accounts (
    account_address     text primary key,
    proxy_wallet        text null,
    display_name        text null,
    created_at_source   timestamptz null,
    first_seen_at       timestamptz not null default now(),
    profile_raw         jsonb not null,
    updated_at          timestamptz not null default now()
);

create table if not exists account_actions (
    id                  bigserial primary key,
    action_fingerprint  text not null unique,
    source_run_id       bigint not null references source_runs(id),
    account_address     text not null,
    proxy_wallet        text null,
    action_type         text not null,
    timestamp_raw       bigint null,
    timestamp_utc       timestamptz null,
    timestamp_kst       timestamp null,
    date_kst            date null,
    transaction_hash    text null,
    condition_id        text null,
    market_slug         text null,
    event_slug          text null,
    token_id            text null,
    asset               text null,
    outcome             text null,
    outcome_index       integer null,
    side                text null,
    price               numeric(38,18) null,
    size                numeric(38,18) null,
    usdc_size           numeric(38,18) null,
    amount              numeric(38,18) null,
    title               text null,
    raw_response_id     bigint not null references raw_api_responses(id),
    raw_row             jsonb not null,
    created_at          timestamptz not null default now()
);

create table if not exists positions_current (
    id                  bigserial primary key,
    source_run_id       bigint not null references source_runs(id),
    account_address     text not null,
    proxy_wallet        text null,
    condition_id        text null,
    market_slug         text null,
    event_slug          text null,
    token_id            text null,
    outcome             text null,
    size                numeric(38,18) null,
    avg_price           numeric(38,18) null,
    current_value       numeric(38,18) null,
    cash_pnl            numeric(38,18) null,
    realized_pnl        numeric(38,18) null,
    redeemable          boolean null,
    mergeable           boolean null,
    negative_risk       boolean null,
    raw_response_id     bigint not null references raw_api_responses(id),
    raw_row             jsonb not null,
    snapshot_at         timestamptz not null default now(),
    created_at          timestamptz not null default now(),
    unique (source_run_id, proxy_wallet, condition_id, token_id)
);

create table if not exists positions_closed (
    id                  bigserial primary key,
    source_run_id       bigint not null references source_runs(id),
    account_address     text not null,
    proxy_wallet        text null,
    condition_id        text null,
    market_slug         text null,
    event_slug          text null,
    token_id            text null,
    outcome             text null,
    avg_price           numeric(38,18) null,
    total_bought        numeric(38,18) null,
    realized_pnl        numeric(38,18) null,
    cash_pnl            numeric(38,18) null,
    cur_price           numeric(38,18) null,
    timestamp_utc       timestamptz null,
    end_date            timestamptz null,
    raw_response_id     bigint not null references raw_api_responses(id),
    raw_row             jsonb not null,
    created_at          timestamptz not null default now(),
    unique (source_run_id, proxy_wallet, condition_id, token_id, timestamp_utc)
);

create table if not exists markets_basic (
    condition_id        text primary key,
    market_slug         text null,
    event_slug          text null,
    event_id            text null,
    event_title         text null,
    title               text null,
    category            text null,
    end_date            timestamptz null,
    active              boolean null,
    closed              boolean null,
    negative_risk       boolean null,
    clob_token_ids      text null,
    raw_metadata        jsonb not null,
    updated_at          timestamptz not null default now()
);

create index if not exists idx_account_actions_timestamp_utc  on account_actions(timestamp_utc);
create index if not exists idx_account_actions_date_kst       on account_actions(date_kst);
create index if not exists idx_account_actions_action_type    on account_actions(action_type);
create index if not exists idx_account_actions_condition_id   on account_actions(condition_id);
create index if not exists idx_account_actions_event_slug     on account_actions(event_slug);
create index if not exists idx_account_actions_tx_hash        on account_actions(transaction_hash);
create index if not exists idx_positions_current_condition_id on positions_current(condition_id);
create index if not exists idx_positions_closed_condition_id  on positions_closed(condition_id);
create index if not exists idx_markets_basic_event_slug       on markets_basic(event_slug);
create index if not exists idx_raw_api_responses_run_endpoint on raw_api_responses(source_run_id, endpoint, fetched_at);

-- +goose StatementEnd

-- +goose StatementBegin

create or replace view v_action_counts_by_type as
select action_type, count(*) as n
from account_actions
group by action_type
order by n desc;

create or replace view v_action_counts_by_kst_date as
select date_kst, count(*) as n
from account_actions
where date_kst is not null
group by date_kst
order by date_kst desc;

create or replace view v_may_2026_action_counts as
select action_type, count(*) as n
from account_actions
where date_kst >= date '2026-05-01'
  and date_kst <  date '2026-06-01'
group by action_type
order by n desc;

create or replace view v_buy_sell_merge_redeem_counts as
select
    sum(case when action_type = 'TRADE'  and lower(coalesce(side,'')) = 'buy'  then 1 else 0 end) as buys,
    sum(case when action_type = 'TRADE'  and lower(coalesce(side,'')) = 'sell' then 1 else 0 end) as sells,
    sum(case when action_type = 'MERGE'  then 1 else 0 end) as merges,
    sum(case when action_type = 'REDEEM' then 1 else 0 end) as redeems,
    sum(case when action_type = 'SPLIT'  then 1 else 0 end) as splits,
    sum(case when action_type = 'CONVERSION' then 1 else 0 end) as conversions,
    sum(case when action_type = 'MAKER_REBATE' then 1 else 0 end) as maker_rebates,
    sum(case when action_type = 'REWARD' then 1 else 0 end) as rewards,
    sum(case when action_type = 'REFERRAL_REWARD' then 1 else 0 end) as referral_rewards
from account_actions;

create or replace view v_distinct_markets_by_month as
select
    to_char(date_trunc('month', coalesce(timestamp_utc, to_timestamp(0))), 'YYYY-MM') as month_utc,
    count(distinct condition_id) as distinct_markets
from account_actions
where timestamp_utc is not null and condition_id is not null
group by 1
order by 1 desc;

create or replace view v_distinct_events_by_month as
select
    to_char(date_trunc('month', coalesce(timestamp_utc, to_timestamp(0))), 'YYYY-MM') as month_utc,
    count(distinct event_slug) as distinct_events
from account_actions
where timestamp_utc is not null and event_slug is not null
group by 1
order by 1 desc;

create or replace view v_latest_active_kst_day as
select date_kst, count(*) as n
from account_actions
where date_kst is not null
group by date_kst
order by date_kst desc
limit 1;

create or replace view v_latest_activity_rows as
select id, action_type, side, price, size, usdc_size, condition_id, market_slug,
       event_slug, title, timestamp_utc, timestamp_kst, date_kst, transaction_hash
from account_actions
order by timestamp_utc desc nulls last
limit 50;

create or replace view v_current_position_counts as
select count(*) as n_open_rows,
       count(distinct condition_id) as n_distinct_markets
from positions_current;

create or replace view v_closed_position_counts as
select count(*) as n_closed_rows,
       count(distinct condition_id) as n_distinct_markets
from positions_closed;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

drop view if exists v_closed_position_counts;
drop view if exists v_current_position_counts;
drop view if exists v_latest_activity_rows;
drop view if exists v_latest_active_kst_day;
drop view if exists v_distinct_events_by_month;
drop view if exists v_distinct_markets_by_month;
drop view if exists v_buy_sell_merge_redeem_counts;
drop view if exists v_may_2026_action_counts;
drop view if exists v_action_counts_by_kst_date;
drop view if exists v_action_counts_by_type;

drop table if exists markets_basic;
drop table if exists positions_closed;
drop table if exists positions_current;
drop table if exists account_actions;
drop table if exists accounts;
drop table if exists raw_api_responses;
drop table if exists source_runs;

-- +goose StatementEnd
