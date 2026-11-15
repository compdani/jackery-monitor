package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func legacyFixture(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.db")
	attachment := filepath.Join(dir, "old.txt")
	if err := os.WriteFile(attachment, []byte("synthetic attachment"), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		`CREATE TABLE devices(device_sn TEXT PRIMARY KEY,name TEXT,model_code INTEGER,last_seen INTEGER,capacity_wh_override INTEGER)`,
		`INSERT INTO devices VALUES('SN','Rig',22,1700000060,9000)`,
		`CREATE TABLE samples(device_sn TEXT,bucket INTEGER,input_wh REAL,output_wh REAL,solar_wh REAL,ac_input_wh REAL,solar_charge_diverted_wh REAL,last_input_w REAL,last_output_w REAL,last_solar_w REAL,last_ac_input_w REAL,last_battery_pct REAL,sample_count INTEGER,PRIMARY KEY(device_sn,bucket))`,
		`INSERT INTO samples VALUES('SN',1700000040,100,80,60,40,7,99999,88888,77777,66666,30,17)`,
		`INSERT INTO samples VALUES('SN',1700000100,20,10,5,15,1,2,3,4,5,40,3)`,
		`CREATE TABLE weather_observations(ts INTEGER PRIMARY KEY,ghi_w_m2 REAL,cloud_cover_pct REAL)`,
		`INSERT INTO weather_observations VALUES(1700000000,500,20)`,
		`CREATE TABLE weather_forecast(ts INTEGER PRIMARY KEY,ghi_w_m2 REAL,cloud_cover_pct REAL,fetched_at INTEGER)`,
		`INSERT INTO weather_forecast VALUES(1700003600,600,10,1700000000)`,
		`CREATE TABLE daily_solar_summary(date TEXT,device_sn TEXT,updated_at INTEGER,actual_sunset_soc_pct REAL,PRIMARY KEY(date,device_sn))`,
		`INSERT INTO daily_solar_summary VALUES('2023-11-14','SN',1700000000,33)`,
		`CREATE TABLE battery_packs(ts INTEGER,parent_sn TEXT,pack_sn TEXT,soc_pct REAL,PRIMARY KEY(ts,parent_sn,pack_sn))`,
		`INSERT INTO battery_packs VALUES(1700000040,'SN','P1',90),(1700000040,'SN','P2',60)`,
		`CREATE TABLE forecast_predictions(device_sn TEXT,made_at INTEGER,target INTEGER,predicted_soc REAL,PRIMARY KEY(device_sn,made_at,target))`,
		`INSERT INTO forecast_predictions VALUES('SN',1699996400,1700000040,50)`,
		`CREATE TABLE device_params(device_sn TEXT,key TEXT,value REAL,source TEXT,updated_at INTEGER,PRIMARY KEY(device_sn,key))`,
		`INSERT INTO device_params VALUES('SN','battery_capacity_wh',9000,'user',1700000000)`,
		`CREATE TABLE algorithm_suggestions(id INTEGER PRIMARY KEY,created_at INTEGER,device_sn TEXT,proposed_value TEXT,status TEXT)`,
		`INSERT INTO algorithm_suggestions VALUES(1,1700000000,'SN','42','pending')`,
		`CREATE TABLE algorithm_changes(id INTEGER PRIMARY KEY,suggestion_id INTEGER,applied_at INTEGER,device_sn TEXT,new_value TEXT)`,
		`INSERT INTO algorithm_changes VALUES(2,1,1700000000,'SN','42')`,
		`CREATE TABLE assistant_conversations(id TEXT PRIMARY KEY,title TEXT,created_at INTEGER)`,
		`INSERT INTO assistant_conversations VALUES('conv','Synthetic',1700000000)`,
		`CREATE TABLE assistant_messages(id TEXT PRIMARY KEY,conversation_id TEXT,role TEXT,content TEXT,parts_json TEXT,created_at INTEGER)`,
		`INSERT INTO assistant_messages VALUES('msg','conv','user','hello','{"attachment_ids":["att"]}',1700000000)`,
		`CREATE TABLE assistant_tool_runs(id TEXT PRIMARY KEY,conversation_id TEXT,message_id TEXT,args_json TEXT,result_json TEXT,created_at INTEGER)`,
		`INSERT INTO assistant_tool_runs VALUES('tool','conv','msg','{"x":1}','{"ok":true}',1700000000)`,
		`CREATE TABLE assistant_attachments(id TEXT PRIMARY KEY,conversation_id TEXT,message_id TEXT,path TEXT,size INTEGER,created_at INTEGER)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(statement, err)
		}
	}
	if _, err := db.Exec(`INSERT INTO assistant_attachments VALUES('att','conv','msg',?,20,1700000000)`, attachment); err != nil {
		t.Fatal(err)
	}
	return path, attachment
}
func TestDomainHistoryImportReconciliationAndRerun(t *testing.T) {
	s := predictionStore(t)
	path, attachment := legacyFixture(t)
	before, _ := os.ReadFile(path)
	originalAttachment, _ := os.ReadFile(attachment)
	var nilStore *Store
	report, err := nilStore.ImportHistory(context.Background(), path, HistoryImportOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if report["samples"].SourceWh["output_wh"] != 90 || report["samples"].DestinationCount != 0 {
		t.Fatal(report)
	}
	for i := 0; i < 2; i++ {
		report, err = s.ImportHistory(context.Background(), path, HistoryImportOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for name, r := range report {
			if r.SourceCount != r.DestinationCount {
				t.Fatal(name, r)
			}
			if name == "samples" {
				for f, v := range r.SourceWh {
					if r.DestinationWh[f] != v {
						t.Fatal(f, r)
					}
				}
			}
		}
	}
	minutes, err := s.EnergyMinutes("SN", 0)
	if err != nil || len(minutes) != 2 {
		t.Fatal(minutes, err)
	}
	totals := EnergyTotals(minutes, 1700000200, 0)["lifetime"].(map[string]any)
	if totals["output_wh"] != 90.0 || totals["input_wh"] != 120.0 {
		t.Fatal("aggregates reintegrated", totals)
	}
	rows, _ := s.List("samples", "SN", 0, 0)
	if rows[0]["last_output_w"] != 88888.0 || rows[0]["sample_count"] != 17.0 {
		t.Fatal(rows)
	}
	packs, _ := s.List("battery_packs", "SN", 0, 0)
	actual, ok := socAt(rows, packs, 1700000040, 1000, 1000)
	if !ok || actual != (35.0+90+60)/3 {
		t.Fatal("multiple packs lost", actual)
	}
	attachmentRow, err := s.Get("attachments", "att")
	if err != nil {
		t.Fatal(err)
	}
	copied, _ := os.ReadFile(attachmentRow["path"].(string))
	if !bytes.Equal(copied, originalAttachment) || attachmentRow["path"] == attachment {
		t.Fatal("attachment not copied", attachmentRow)
	}
	messages, _ := s.List("messages", "conv", 0, 0)
	if len(messages) != 1 || messages[0]["parts"] == nil {
		t.Fatal(messages)
	}
	device, _ := s.Get("devices", "SN")
	if device["capacity_wh_override"] != 9000.0 {
		t.Fatal(device)
	}
	if _, err = s.CorrectOutput("SN", 1700000040, 1700000100, 600); err != nil {
		t.Fatal(err)
	}
	report, err = s.ImportHistory(context.Background(), path, HistoryImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if report["samples"].SourceWh["output_wh"] != 90 || report["samples"].DestinationWh["output_wh"] != 20 {
		t.Fatal("correction lost on reimport", report["samples"])
	}
	after, _ := os.ReadFile(path)
	afterAttachment, _ := os.ReadFile(attachment)
	if !bytes.Equal(before, after) || !bytes.Equal(originalAttachment, afterAttachment) {
		t.Fatal("source modified")
	}
	for _, name := range []string{"weather_observations", "weather_forecasts", "suggestions", "changes"} {
		records, err := s.List(name, "", 0, 0)
		if err != nil || len(records) != 1 {
			t.Fatal(name, records, err)
		}
	}
}
func TestListBeyond100000AndStableEqualTimestampPagination(t *testing.T) {
	s := predictionStore(t)
	_, err := s.App.DB().NewQuery(`WITH RECURSIVE n(x) AS (VALUES(0) UNION ALL SELECT x+1 FROM n WHERE x<100004) INSERT INTO samples(id,key,device_sn,ts,payload) SELECT printf('p%014d',x),printf('p%014d',x),'SN',100,json_object('ts',100,'ordinal',x) FROM n`).Execute()
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.List("samples", "SN", 0, 0)
	if err != nil || len(rows) != 100005 {
		t.Fatal(len(rows), err)
	}
	if rows[100004]["ordinal"] != 100004.0 {
		t.Fatal(rows[100004])
	}
	limited, err := s.List("samples", "SN", 0, 100002)
	if err != nil || len(limited) != 100002 {
		t.Fatal(len(limited), err)
	}
}
func TestHistoryImportMutableSourceUsesSameKey(t *testing.T) {
	s := predictionStore(t)
	path, _ := legacyFixture(t)
	if _, err := s.ImportHistory(context.Background(), path, HistoryImportOptions{}); err != nil {
		t.Fatal(err)
	}
	db, _ := sql.Open("sqlite", path)
	_, err := db.Exec(`UPDATE samples SET output_wh=81 WHERE bucket=1700000040`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	report, err := s.ImportHistory(context.Background(), path, HistoryImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := s.List("samples", "SN", 0, 0)
	if len(rows) != 2 || report["samples"].DestinationWh["output_wh"] != 91 {
		t.Fatal(report)
	}
	// Reports are JSON-safe and retain distinct source/destination totals.
	if _, err := json.Marshal(report); err != nil {
		t.Fatal(err)
	}
}
func TestHistoryUpgradeRemovesOnlyGeneratedArchiveDuplicates(t *testing.T) {
	s := predictionStore(t)
	path, _ := legacyFixture(t)
	if err := s.Put("devices", "legacy:devices:old-content-hash", "SN", map[string]any{"device": map[string]any{"device_sn": "SN"}, "ts": 1700000060.0}); err != nil {
		t.Fatal(err)
	}
	if err := s.Put("samples", "legacy:samples:old-content-hash", "SN", map[string]any{"device_sn": "SN", "bucket": 1700000040.0, "ts": 1700000040.0, "output_wh": 70.0}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ImportHistory(context.Background(), path, HistoryImportOptions{}); err != nil {
		t.Fatal(err)
	}
	for collection, want := range map[string]int{"devices": 1, "samples": 2} {
		rows, err := s.List(collection, "SN", 0, 0)
		if err != nil || len(rows) != want {
			t.Fatal(collection, len(rows), err)
		}
	}
}
