package wemanage_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aiseat/internal/store"
	"aiseat/internal/testutil"
	"aiseat/internal/wemanage"
	"github.com/google/uuid"
)

func TestObservabilityHTTP(t *testing.T) {
	if !strings.Contains(testutil.TestDBURL(), "/wemanage_diagnostics_test") {
		t.Fatal("requires isolated wemanage_diagnostics_test database")
	}
	ctx := context.Background()
	st := store.New(testutil.NewDB(t))
	if e := wemanage.Install(ctx, st); e != nil {
		t.Fatal(e)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, e := st.Pool.Exec(ctx, q, args...); e != nil {
			t.Fatal(e)
		}
	}
	emp := func(k string) store.Employee {
		x, e := st.UpsertEmployeeByOpenID(ctx, store.Employee{FeishuOpenID: k, Name: k})
		if e != nil {
			t.Fatal(e)
		}
		return x
	}
	admin := emp("admin")
	member := emp("member")
	private := emp("private")
	hidden := emp("hidden")
	exec(`update employees set is_super_admin=true where id=$1`, admin.ID)
	exec(`update employees set private_work_since=now() where id=$1`, private.ID)
	cookie, csrf, e := st.CreateWebSession(ctx, admin.ID, time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	memberCookie, _, e := st.CreateWebSession(ctx, member.ID, time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	srv := httptest.NewServer((&wemanage.Service{Store: st}).Handler())
	defer srv.Close()
	call := func(method, path, body, auth, token string) (int, map[string]any) {
		t.Helper()
		r, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		if auth != "" {
			r.AddCookie(&http.Cookie{Name: "aiseat_sess", Value: auth})
		}
		r.Header.Set("X-CSRF-Token", token)
		resp, e := http.DefaultClient.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		defer resp.Body.Close()
		var result map[string]any
		if e = json.NewDecoder(resp.Body).Decode(&result); e != nil {
			t.Fatal(e)
		}
		return resp.StatusCode, result
	}
	today := time.Now().In(time.FixedZone("CST", 8*3600)).Format(time.DateOnly)
	at, _ := time.Parse(time.RFC3339, today+"T00:01:00+08:00")
	period := "?from=" + today + "&to=" + today
	for _, endpoint := range []string{"historical-usage", "fusion-usage", "usage-diagnostics", "storage-status"} {
		for _, a := range []struct {
			cookie string
			want   int
		}{{"", 401}, {memberCookie, 403}} {
			code, _ := call("GET", "/api/"+endpoint+period, "", a.cookie, "")
			if code != a.want {
				t.Fatal(endpoint, code, a.want)
			}
		}
	}
	exec(`insert into wm_historical_usage_imports values('source-a','test.xlsx',now(),'{}','{}'),('source-b','other.xlsx',now(),'{}','{}')`)
	for i := 1; i <= 101; i++ {
		exec(`insert into wm_historical_usage_daily values('source-a',$1,'2026-09-01','unmapped','model',2,0,100,10,'original','source',2.5,'test','{}')`, i)
	}
	exec(`insert into wm_historical_usage_daily values('source-b',1,'2026-09-01','unmapped','model',999,0,999,999,'original','source',999,'test','{}')`)
	code, j := call("GET", "/api/historical-usage?source=source-a", "", cookie, "")
	if code != 200 || len(j["rows"].([]any)) != 100 || j["total"].(map[string]any)["calls"] != float64(202) {
		t.Fatal(code, j)
	}
	code, j = call("GET", "/api/historical-usage?source=source-a&offset=100", "", cookie, "")
	if code != 200 || len(j["rows"].([]any)) != 1 || j["has_more"] != false {
		t.Fatal(code, j)
	}
	for _, id := range []int64{member.ID, private.ID, hidden.ID} {
		for i := 0; i < 5; i++ {
			exec(`insert into requests(id,employee_id,session_id,started_at,client_kind,endpoint,model_requested,tokens_input,tokens_output,tokens_cached,status_code)values($1,$2,77,$3,'codex','responses','gpt-6-astra',400000,10,0,500)`, uuid.NewString(), id, at)
		}
	}
	var project int64
	if e = st.Pool.QueryRow(ctx, `insert into projects(name,category)values('secret','client')returning id`).Scan(&project); e != nil {
		t.Fatal(e)
	}
	exec(`insert into wm_hidden_projects(project_id,reason)values($1,'private')`, project)
	exec(`insert into request_projects(request_id,project_id)select id,$1 from requests where employee_id=$2`, project, hidden.ID)
	for i := 0; i < 101; i++ {
		exec(`insert into wm_fusion_events(request_id,at,employee_id,model,reason,status,duration_ms,input_tokens,output_tokens,completed,source)values($1,$2,$3,'gpt-6.1-sol','balanced_default',200,100,20,5,true,'{}')`, uuid.NewString(), at, member.ID)
	}
	for _, id := range []int64{private.ID, hidden.ID} {
		exec(`insert into wm_fusion_events(request_id,at,employee_id,model,reason,status,duration_ms,source)values($1,$2,$3,'secret-model','test',200,100,'{}')`, uuid.NewString(), at, id)
	}
	code, j = call("GET", "/api/fusion-usage"+period, "", cookie, "")
	if code != 200 || len(j["rows"].([]any)) != 100 || len(j["models"].([]any)) != 1 {
		t.Fatal(code, j)
	}
	next := j["next"].(map[string]any)
	code, j = call("GET", "/api/fusion-usage"+period+"&before="+strings.ReplaceAll(next["before"].(string), "+", "%2B")+"&after_id="+next["after_id"].(string), "", cookie, "")
	if code != 200 || len(j["rows"].([]any)) != 1 {
		t.Fatal("timestamp tie paging", code, j)
	}
	code, j = call("GET", "/api/usage-diagnostics"+period, "", cookie, "")
	if code != 200 || len(j["rows"].([]any)) != 1 {
		t.Fatal("diagnostic privacy", code, j)
	}
	row := j["rows"].([]any)[0].(map[string]any)
	if len(row["signals"].([]any)) != 4 {
		t.Fatal(row)
	}
	body := fmt.Sprintf(`{"employee_id":%d,"session_id":77,"rule":"repeated_errors","state":"adopted","note":"retry fixed"}`, member.ID)
	code, _ = call("POST", "/api/usage-diagnostics/actions"+period, body, cookie, "")
	if code != 403 {
		t.Fatal("csrf", code)
	}
	code, j = call("POST", "/api/usage-diagnostics/actions"+period, body, cookie, csrf)
	if code != 200 {
		t.Fatal(code, j)
	}
	code, j = call("GET", "/api/usage-diagnostics"+period, "", cookie, "")
	found := false
	for _, v := range j["rows"].([]any)[0].(map[string]any)["signals"].([]any) {
		f := v.(map[string]any)
		if f["rule"] == "repeated_errors" {
			found = f["state"] == "adopted" && f["note"] == "retry fixed"
		}
	}
	if !found {
		t.Fatal("action lost", j)
	}
	body = fmt.Sprintf(`{"employee_id":%d,"session_id":77,"rule":"repeated_errors","state":"adopted"}`, private.ID)
	code, _ = call("POST", "/api/usage-diagnostics/actions"+period, body, cookie, csrf)
	if code != 404 {
		t.Fatal("private action", code)
	}
	exec(`insert into wm_observation_sync(name,detail)values('storage','{"available_bytes":123}')`)
	code, j = call("GET", "/api/storage-status", "", cookie, "")
	if code != 200 || j["status"].(map[string]any)["available_bytes"] != float64(123) {
		t.Fatal(code, j)
	}
}
