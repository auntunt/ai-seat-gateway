package wemanage

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"net/http"
	"strconv"
	"time"
)

const observabilitySchema = `
CREATE TABLE IF NOT EXISTS wm_historical_usage_imports(source_sha256 text PRIMARY KEY,source_path text NOT NULL,imported_at timestamptz NOT NULL DEFAULT now(),metadata jsonb NOT NULL,workbook_cells jsonb NOT NULL);
CREATE TABLE IF NOT EXISTS wm_historical_usage_daily(source_sha256 text NOT NULL REFERENCES wm_historical_usage_imports(source_sha256),source_row integer NOT NULL,day date NOT NULL,account_name text NOT NULL,model text NOT NULL,calls bigint NOT NULL,quota bigint NOT NULL,input_tokens bigint NOT NULL,output_tokens bigint NOT NULL,capture_status text NOT NULL,original_source text NOT NULL,calculated_cny numeric NOT NULL,price_class text NOT NULL,raw_cells jsonb NOT NULL,PRIMARY KEY(source_sha256,source_row));
CREATE INDEX IF NOT EXISTS wm_history_day_account ON wm_historical_usage_daily(day,account_name);
CREATE TABLE IF NOT EXISTS wm_fusion_events(request_id uuid PRIMARY KEY,at timestamptz NOT NULL,employee_id bigint REFERENCES employees(id),api_key_id bigint REFERENCES api_keys(id),model text NOT NULL,reason text NOT NULL,status integer NOT NULL,duration_ms bigint NOT NULL,input_tokens bigint,output_tokens bigint,cached_tokens bigint,completed boolean,source jsonb NOT NULL,imported_at timestamptz NOT NULL DEFAULT now());
CREATE INDEX IF NOT EXISTS wm_fusion_at ON wm_fusion_events(at,employee_id);
CREATE TABLE IF NOT EXISTS wm_observation_sync(name text PRIMARY KEY,at timestamptz NOT NULL DEFAULT now(),detail jsonb NOT NULL);
CREATE TABLE IF NOT EXISTS wm_usage_action_events(id bigserial PRIMARY KEY,employee_id bigint NOT NULL REFERENCES employees(id),session_id bigint NOT NULL,rule text NOT NULL,from_day date NOT NULL,to_day date NOT NULL,state text NOT NULL CHECK(state IN ('pending','adopted','not_applicable')),note text NOT NULL,editor_id bigint NOT NULL REFERENCES employees(id),created_at timestamptz NOT NULL DEFAULT now());
CREATE INDEX IF NOT EXISTS wm_usage_action_lookup ON wm_usage_action_events(employee_id,session_id,rule,from_day,to_day,id DESC);
`

func (s *Service) historicalUsage(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	month := r.URL.Query().Get("month")
	if month != "" {
		d, e := time.Parse("2006-01", month)
		if e != nil || d.Format("2006-01") != month {
			fail(w, 400, "月份无效")
			return
		}
	}
	offset := 0
	if v := r.URL.Query().Get("offset"); v != "" {
		n, e := strconv.Atoi(v)
		if e != nil || n < 0 || n > 1000000 {
			fail(w, 400, "分页参数无效")
			return
		}
		offset = n
	}
	account := r.URL.Query().Get("account")
	source := r.URL.Query().Get("source")
	// Pick one immutable import; never sum overlapping snapshots.
	if source == "" {
		if e := s.Store.Pool.QueryRow(ctx, `select source_sha256 from wm_historical_usage_imports order by imported_at desc,source_sha256 limit 1`).Scan(&source); e != nil {
			if !errors.Is(e, pgx.ErrNoRows) {
				fail(w, 500, "读取历史来源失败")
				return
			}
			reply(w, 200, map[string]any{"sources": []any{}, "rows": []any{}, "total": nil})
			return
		}
	}
	rows, e := s.Store.Pool.Query(ctx, `select source_sha256,imported_at,metadata from wm_historical_usage_imports order by imported_at desc`)
	if e != nil {
		fail(w, 500, "读取历史来源失败")
		return
	}
	sources := []map[string]any{}
	found := false
	for rows.Next() {
		var sha string
		var at time.Time
		var meta json.RawMessage
		if e = rows.Scan(&sha, &at, &meta); e != nil {
			break
		}
		if sha == source {
			found = true
		}
		sources = append(sources, map[string]any{"sha256": sha, "imported_at": at, "metadata": meta})
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		fail(w, 500, "读取历史来源失败")
		return
	}
	if !found {
		fail(w, 404, "来源不存在")
		return
	}
	filter := ` from wm_historical_usage_daily where source_sha256=$1 and ($2='' or to_char(day,'YYYY-MM')=$2) and ($3='' or account_name=$3)`
	var count, calls, input, output, accounts int64
	var cost string
	e = s.Store.Pool.QueryRow(ctx, `select count(*),coalesce(sum(calls),0),coalesce(sum(input_tokens),0),coalesce(sum(output_tokens),0),count(distinct account_name),coalesce(sum(calculated_cny),0)::text`+filter, source, month, account).Scan(&count, &calls, &input, &output, &accounts, &cost)
	if e != nil {
		fail(w, 500, "读取历史汇总失败")
		return
	}
	rs, e := s.Store.Pool.Query(ctx, `select source_row,day::text,account_name,model,calls,input_tokens,output_tokens,calculated_cny::text,capture_status,price_class`+filter+` order by day desc,source_row limit 100 offset $4`, source, month, account, offset)
	if e != nil {
		fail(w, 500, "读取历史明细失败")
		return
	}
	items := []map[string]any{}
	for rs.Next() {
		var row int
		var day, a, m, c, stat, price string
		var n, i, o int64
		if e = rs.Scan(&row, &day, &a, &m, &n, &i, &o, &c, &stat, &price); e != nil {
			break
		}
		items = append(items, map[string]any{"source_row": row, "day": day, "account": a, "model": m, "calls": n, "input": i, "output": o, "calculated_cny": c, "capture_status": stat, "price_class": price})
	}
	if e == nil {
		e = rs.Err()
	}
	rs.Close()
	if e != nil {
		fail(w, 500, "读取历史明细失败")
		return
	}
	options := []string{}
	ors, e := s.Store.Pool.Query(ctx, `select distinct account_name from wm_historical_usage_daily where source_sha256=$1 order by 1`, source)
	if e != nil {
		fail(w, 500, "读取历史账号失败")
		return
	}
	for ors.Next() {
		var a string
		if e = ors.Scan(&a); e != nil {
			break
		}
		options = append(options, a)
	}
	if e == nil {
		e = ors.Err()
	}
	ors.Close()
	if e != nil {
		fail(w, 500, "读取历史账号失败")
		return
	}
	reply(w, 200, map[string]any{"sources": sources, "source": source, "accounts": options, "rows": items, "offset": offset, "has_more": int64(offset+len(items)) < count, "total": map[string]any{"rows": count, "calls": calls, "input": input, "output": output, "accounts": accounts, "calculated_cny": cost}, "note": "历史账号归属待确认，独立于网关逐次请求统计；金额为约定价测算，不是付款凭证。"})
}

// Fusion has no gateway request FK. Withhold entire employees with hidden-project
// activity in the requested period rather than guessing which route was private.
const fusionVisible = `e.private_work_since is null and e.name<>'林伊默' and not exists(select 1 from requests r left join api_keys k on k.id=r.api_key_id left join request_projects rp on rp.request_id=r.id join wm_hidden_projects h on h.project_id=coalesce(rp.project_id,k.project_id) where r.employee_id=e.id and r.started_at >= $1 and r.started_at < $2)`

func (s *Service) fusionUsage(w http.ResponseWriter, r *http.Request) {
	d, ok := s.diagnosticRange(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	cursor := r.URL.Query().Get("before")
	var before time.Time
	if cursor != "" {
		var e error
		before, e = time.Parse(time.RFC3339Nano, cursor)
		if e != nil {
			fail(w, 400, "分页时间无效")
			return
		}
	} else {
		before = d.To
	}
	base := ` from wm_fusion_events f join employees e on e.id=f.employee_id where f.at >= $1 and f.at < $2 and ($3::bigint=0 or e.id=$3) and ` + fusionVisible
	rows, e := s.Store.Pool.Query(ctx, `select model,count(*),coalesce(sum(input_tokens),0),coalesce(sum(output_tokens),0),coalesce(sum(cached_tokens),0),count(*) filter(where f.status>=400 or completed=false),count(*) filter(where input_tokens is null or output_tokens is null)`+base+` group by model order by count(*) desc`, d.From, d.To, d.Employee)
	if e != nil {
		fail(w, 500, "读取Fusion汇总失败")
		return
	}
	models := []map[string]any{}
	for rows.Next() {
		var m string
		var n, i, o, c, errN, unknown int64
		if e = rows.Scan(&m, &n, &i, &o, &c, &errN, &unknown); e != nil {
			break
		}
		models = append(models, map[string]any{"model": m, "requests": n, "input": i, "output": o, "cached": c, "errors": errN, "unknown_usage": unknown})
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		fail(w, 500, "读取Fusion汇总失败")
		return
	}
	// Cursor uses a stable tuple; request id breaks timestamp ties.
	afterID := r.URL.Query().Get("after_id")
	if afterID == "" {
		afterID = "ffffffff-ffff-ffff-ffff-ffffffffffff"
	}
	if _, e := uuid.Parse(afterID); e != nil {
		fail(w, 400, "分页编号无效")
		return
	}
	rs, e := s.Store.Pool.Query(ctx, `select f.request_id::text,f.at,e.id,e.name,model,reason,f.status,duration_ms,input_tokens,output_tokens,cached_tokens,completed`+base+` and (f.at,f.request_id)<($4,$5::uuid) order by f.at desc,f.request_id desc limit 101`, d.From, d.To, d.Employee, before, afterID)
	if e != nil {
		fail(w, 500, "读取Fusion记录失败")
		return
	}
	items := []map[string]any{}
	for rs.Next() {
		var id, name, m, reason string
		var at time.Time
		var emp int64
		var status, ms int
		var i, o, c *int64
		var done *bool
		if e = rs.Scan(&id, &at, &emp, &name, &m, &reason, &status, &ms, &i, &o, &c, &done); e != nil {
			break
		}
		items = append(items, map[string]any{"id": id, "at": at, "employee_id": emp, "employee": name, "model": m, "reason": reason, "status": status, "duration_ms": ms, "input": i, "output": o, "cached": c, "completed": done})
	}
	if e == nil {
		e = rs.Err()
	}
	rs.Close()
	if e != nil {
		fail(w, 500, "读取Fusion记录失败")
		return
	}
	next := map[string]any{}
	if len(items) > 100 {
		items = items[:100]
		last := items[99]
		next = map[string]any{"before": last["at"], "after_id": last["id"]}
	}
	var syncAt *time.Time
	_ = s.Store.Pool.QueryRow(ctx, `select at from wm_observation_sync where name='fusion'`).Scan(&syncAt)
	var unmatched int64
	_ = s.Store.Pool.QueryRow(ctx, `select count(*) from wm_fusion_events where at >= $1 and at < $2 and employee_id is null`, d.From, d.To).Scan(&unmatched)
	reply(w, 200, map[string]any{"models": models, "rows": items, "next": next, "synced_at": syncAt, "unmatched": unmatched, "note": "来自Fusion独立路由日志，不与网关用量重复相加。没有请求级项目关联的人员，如在所选时间参与隐藏项目，其路由记录保守隐藏。"})
}
func (s *Service) storageStatus(w http.ResponseWriter, r *http.Request) {
	var at time.Time
	var raw json.RawMessage
	if e := s.Store.Pool.QueryRow(r.Context(), `select at,detail from wm_observation_sync where name='storage'`).Scan(&at, &raw); e != nil {
		if !errors.Is(e, pgx.ErrNoRows) {
			fail(w, 500, "读取存储状态失败")
			return
		}
		reply(w, 200, map[string]any{"measured_at": nil})
		return
	}
	reply(w, 200, map[string]any{"measured_at": at, "status": raw})
}
