package wemanage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

type usageSignal struct {
	Rule     string     `json:"rule"`
	Title    string     `json:"title"`
	Evidence string     `json:"evidence"`
	Action   string     `json:"action"`
	Verify   string     `json:"verify"`
	State    string     `json:"state"`
	Note     string     `json:"note"`
	Updated  *time.Time `json:"updated_at,omitempty"`
}
type sessionDiagnostic struct {
	EmployeeID int64         `json:"employee_id"`
	Employee   string        `json:"employee"`
	SessionID  int64         `json:"session_id"`
	Requests   int64         `json:"requests"`
	Input      int64         `json:"input"`
	Cached     int64         `json:"cached"`
	Output     int64         `json:"output"`
	Errors     int64         `json:"errors"`
	Strong     int64         `json:"strong"`
	First      time.Time     `json:"first"`
	Last       time.Time     `json:"last"`
	Signals    []usageSignal `json:"signals"`
}

func evaluateUsage(x sessionDiagnostic) []usageSignal {
	out := []usageSignal{}
	add := func(rule, title, evidence, action, verify string) {
		out = append(out, usageSignal{Rule: rule, Title: title, Evidence: evidence, Action: action, Verify: verify, State: "pending"})
	}
	if x.Requests >= 3 && x.Input/x.Requests >= 32000 && float64(x.Cached) < float64(x.Input)*.2 {
		add("low_cache_context", "长上下文且缓存复用较低", fmt.Sprintf("%d 次调用，平均输入 %d Token，缓存占输入 %.1f%%。", x.Requests, x.Input/x.Requests, 100*float64(x.Cached)/float64(x.Input)), "核对是否反复携带无关材料。保持稳定指令与工具顺序；独立任务另开会话；需要交接时保留目标、关键结论和未完成事项。不要直接删除仍需使用的上下文。", "同类任务对比平均未缓存输入、缓存比例，同时确认任务质量未下降。")
	}
	if x.Requests >= 5 && float64(x.Strong) >= float64(x.Requests)*.8 {
		add("strong_model_concentration", "高档模型持续使用", fmt.Sprintf("%d 次调用中，%d 次使用 Astra。单凭用量不能断定选型不当。", x.Requests, x.Strong), "查看任务内容是否确需复杂推理。下一个独立任务可用 Fusion；保留需要高能力模型的任务，不在工具调用中途切换。", "比较同类任务的完成质量、重试次数和实际模型分布；不能将 Token 数直接视作实付费用。")
	}
	if x.Errors >= 3 && float64(x.Errors) >= float64(x.Requests)*.1 {
		add("repeated_errors", "会话中多次调用失败", fmt.Sprintf("%d 次调用中有 %d 次错误。原因可能是上游限额、网络或请求格式。", x.Requests, x.Errors), "先检查原始请求的错误码。限额应等待恢复，格式错误应修正参数；避免在原因不明时持续重试。", "对比错误率与成功请求数；上游故障不能归因于员工操作。")
	}
	if x.Requests >= 3 && x.Input >= 1000000 && float64(x.Output) < float64(x.Input)*.005 {
		add("input_dominant", "输入远多于输出", fmt.Sprintf("输入 %d Token，输出 %d Token。长文审阅等任务也可能合理出现这种比例。", x.Input, x.Output), "检查日志中是否反复提交完整文件或长工具结果。可使用有来源定位的相关片段；不要为省 Token 丢失任务必需信息。", "比较每次成功请求的输入量与结果质量，并核对是否出现需要返工的信息遗漏。")
	}
	return out
}
func validUsageRule(s string) bool {
	switch s {
	case "low_cache_context", "strong_model_concentration", "repeated_errors", "input_dominant":
		return true
	}
	return false
}
func (s *Service) usageDiagnostics(w http.ResponseWriter, r *http.Request) {
	d, ok := s.diagnosticRange(w, r)
	if !ok {
		return
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
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	rows, e := s.Store.Pool.Query(ctx, `select e.id,e.name,r.session_id,count(*),coalesce(sum(tokens_input),0),coalesce(sum(tokens_cached),0),coalesce(sum(tokens_output),0),count(*) filter(where status_code>=400 or coalesce(error_type,'')<>''),count(*) filter(where model_requested like '%-astra'),min(r.started_at),max(r.started_at)`+diagnosticJoins+diagnosticFilter+` and r.session_id>0 group by e.id,e.name,r.session_id order by sum(tokens_input) desc nulls last,e.id,r.session_id limit 101 offset $4`, d.From, d.To, d.Employee, offset)
	if e != nil {
		fail(w, 500, "读取会话诊断失败")
		return
	}
	items := []sessionDiagnostic{}
	for rows.Next() {
		var x sessionDiagnostic
		if e = rows.Scan(&x.EmployeeID, &x.Employee, &x.SessionID, &x.Requests, &x.Input, &x.Cached, &x.Output, &x.Errors, &x.Strong, &x.First, &x.Last); e != nil {
			break
		}
		x.Signals = evaluateUsage(x)
		items = append(items, x)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		fail(w, 500, "读取会话诊断失败")
		return
	}
	more := len(items) > 100
	if more {
		items = items[:100]
	}
	from := d.From.Format(time.DateOnly)
	to := r.URL.Query().Get("to")
	if to == "" {
		loc, _ := time.LoadLocation("Asia/Shanghai")
		to = time.Now().In(loc).Format(time.DateOnly)
	}
	actions := map[string]usageSignal{}
	ar, e := s.Store.Pool.Query(ctx, `select distinct on(employee_id,session_id,rule)employee_id,session_id,rule,state,note,created_at from wm_usage_action_events where from_day=$1::date and to_day=$2::date order by employee_id,session_id,rule,id desc`, from, to)
	if e != nil {
		fail(w, 500, "读取处理记录失败")
		return
	}
	for ar.Next() {
		var emp, sid int64
		var x usageSignal
		var at time.Time
		if e = ar.Scan(&emp, &sid, &x.Rule, &x.State, &x.Note, &at); e != nil {
			break
		}
		x.Updated = &at
		actions[fmt.Sprintf("%d/%d/%s", emp, sid, x.Rule)] = x
	}
	if e == nil {
		e = ar.Err()
	}
	ar.Close()
	if e != nil {
		fail(w, 500, "读取处理记录失败")
		return
	}
	for i := range items {
		for j := range items[i].Signals {
			f := &items[i].Signals[j]
			if a, exists := actions[fmt.Sprintf("%d/%d/%s", items[i].EmployeeID, items[i].SessionID, f.Rule)]; exists {
				f.State, f.Note, f.Updated = a.State, a.Note, a.Updated
			}
		}
	}
	totals := []map[string]any{}
	for _, period := range []struct{ From, To time.Time }{{d.From, d.To}, {d.PreviousFrom, d.PreviousTo}} {
		var n, input, cached, output, errs int64
		e = s.Store.Pool.QueryRow(ctx, `select count(*),coalesce(sum(tokens_input),0),coalesce(sum(tokens_cached),0),coalesce(sum(tokens_output),0),count(*) filter(where status_code>=400 or coalesce(error_type,'')<>'')`+diagnosticJoins+diagnosticFilter, period.From, period.To, d.Employee).Scan(&n, &input, &cached, &output, &errs)
		if e != nil {
			fail(w, 500, "读取对照指标失败")
			return
		}
		totals = append(totals, map[string]any{"from": period.From, "to": period.To, "requests": n, "input": input, "cached": cached, "output": output, "errors": errs})
	}
	reply(w, 200, map[string]any{"rows": items, "offset": offset, "has_more": more, "current": totals[0], "previous": totals[1], "note": "规则生成待核查线索，不是员工评分。前后时间段任务量和难度可能不同，指标变化不等于改进措施的因果效果。"})
}
func (s *Service) saveUsageAction(w http.ResponseWriter, r *http.Request) {
	if !s.managementWriter(w, r) {
		return
	}
	d, ok := s.diagnosticRange(w, r)
	if !ok {
		return
	}
	var q struct {
		Employee int64  `json:"employee_id"`
		Session  int64  `json:"session_id"`
		Rule     string `json:"rule"`
		State    string `json:"state"`
		Note     string `json:"note"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16000)).Decode(&q) != nil || q.Employee < 1 || q.Session < 1 || !validUsageRule(q.Rule) || len(q.Note) > 8000 || (q.State != "pending" && q.State != "adopted" && q.State != "not_applicable") {
		fail(w, 400, "处理记录格式无效")
		return
	}
	var exists bool
	e := s.Store.Pool.QueryRow(r.Context(), `select exists(select 1 `+diagnosticJoins+diagnosticFilter+` and r.session_id=$4)`, d.From, d.To, q.Employee, q.Session).Scan(&exists)
	if e != nil {
		fail(w, 500, "核对会话失败")
		return
	}
	if !exists {
		fail(w, 404, "会话不存在或不可见")
		return
	}
	loc, _ := time.LoadLocation("Asia/Shanghai")
	to := r.URL.Query().Get("to")
	if to == "" {
		to = time.Now().In(loc).Format(time.DateOnly)
	}
	_, e = s.Store.Pool.Exec(r.Context(), `insert into wm_usage_action_events(employee_id,session_id,rule,from_day,to_day,state,note,editor_id)values($1,$2,$3,$4::date,$5::date,$6,$7,$8)`, q.Employee, q.Session, q.Rule, d.From.Format(time.DateOnly), to, q.State, q.Note, who(r).employee.ID)
	if e != nil {
		fail(w, 500, "保存处理记录失败")
		return
	}
	reply(w, 200, map[string]bool{"ok": true})
}
