package gateway

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestSubscriptionCompletionFields(t *testing.T) {
	f := newFixture(t)
	token := f.login("subscriber")
	n := f.pair(token)
	n.connect()
	n.inventory()
	_, sid, _ := n.create()
	ctx := context.Background()
	var uid string
	if e := f.db.QueryRow(ctx, `SELECT user_id FROM sessions WHERE id=$1`, sid).Scan(&uid); e != nil {
		t.Fatal(e)
	}
	if _, e := f.db.Exec(ctx, `UPDATE sessions SET title='fixture task' WHERE id=$1`, sid); e != nil {
		t.Fatal(e)
	}
	f.app.cfg.AuthMode = "wechat"
	f.app.cfg.Subscriptions = []SubscriptionTemplateConfig{{ID: "fixture-template", Title: "Completed", Kind: "completed", State: "formal", Data: map[string]SubscriptionField{"thing1": {Source: "session_title", Limit: 20}, "thing2": {Source: "fixed", Value: "Task completed", Limit: 20}, "time5": {Source: "time", Limit: 20}}}}
	if _, e := f.db.Exec(ctx, `INSERT INTO subscription_identities(user_id,encrypted_subject) VALUES($1,$2)`, uid, []byte("fixture-only")); e != nil {
		t.Fatal(e)
	}
	if _, e := f.db.Exec(ctx, `INSERT INTO subscription_consents(user_id,template_id,credits,decision) VALUES($1,'fixture-template',1,'accept')`, uid); e != nil {
		t.Fatal(e)
	}
	tx, e := f.db.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	if e = f.app.notify(ctx, tx, uid, sid, "completed", "status"); e != nil {
		t.Fatal(e)
	}
	var raw []byte
	if e = tx.QueryRow(ctx, `SELECT payload FROM subscription_outbox WHERE user_id=$1`, uid).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	var payload M
	if e = json.Unmarshal(raw, &payload); e != nil {
		t.Fatal(e)
	}
	data := object(payload, "data")
	if str(object(data, "thing1"), "value") != "fixture task" {
		t.Fatal("task name missing")
	}
	if str(payload, "page") != "pages/session/index?id="+sid {
		t.Fatal("wrong landing page")
	}
	var credits int
	if e = tx.QueryRow(ctx, `SELECT credits FROM subscription_consents WHERE user_id=$1`, uid).Scan(&credits); e != nil || credits != 0 {
		t.Fatal("one credit must be consumed")
	}
}
func TestSubscriptionTimeShanghai(t *testing.T) {
	if subscriptionTime(time.Date(2026, 9, 10, 18, 30, 0, 0, time.UTC)) != "2026-09-11 02:30" {
		t.Fatal("wrong local completion time")
	}
}
