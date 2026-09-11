package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
	"weagent/backend/internal/fakenode"
)

// Re-exec the test binary as a genuinely separate Node process; no Gateway memory is shared.
func TestFakeNodeProcess(t *testing.T) {
	if os.Getenv("WEAGENT_FAKE_CHILD") != "1" {
		return
	}
	ctx, c := context.WithTimeout(context.Background(), 60*time.Second)
	defer c()
	e := fakenode.Run(ctx, fakenode.Config{URL: os.Getenv("WEAGENT_FAKE_URL"), Dir: os.Getenv("WEAGENT_FAKE_DIR"), Name: "Independent Fake Node", Output: os.Stdout, Once: true, FaultAfterAccept: os.Getenv("WEAGENT_FAKE_FAULT") == "1"})
	if e != nil {
		t.Log("Fake Node process ended without replaying uncertain native work")
	}
}

type childNode struct {
	cmd    *exec.Cmd
	events chan M
	done   chan error
	stderr *bytes.Buffer
}

func startFakeChild(t *testing.T, base, dir string, fault bool) *childNode {
	t.Helper()
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	cmd := exec.Command(exe, "-test.run=^TestFakeNodeProcess$", "-test.timeout=70s")
	cmd.Env = append(os.Environ(), "WEAGENT_FAKE_CHILD=1", "WEAGENT_FAKE_URL="+base, "WEAGENT_FAKE_DIR="+dir)
	if fault {
		cmd.Env = append(cmd.Env, "WEAGENT_FAKE_FAULT=1")
	}
	stdout, e := cmd.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	child := &childNode{cmd: cmd, events: make(chan M, 100), done: make(chan error, 1), stderr: stderr}
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			var m M
			if json.Unmarshal(scanner.Bytes(), &m) == nil {
				child.events <- m
			}
		}
		close(child.events)
		child.done <- cmd.Wait()
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		select {
		case <-child.done:
		case <-time.After(5 * time.Second):
			t.Error("Fake Node child failed to stop")
		}
	})
	return child
}
func (c *childNode) wait(t *testing.T, event string) M {
	t.Helper()
	timer := time.NewTimer(12 * time.Second)
	defer timer.Stop()
	for {
		select {
		case m, ok := <-c.events:
			if !ok {
				t.Fatalf("Fake Node exited while awaiting %s", event)
			}
			if str(m, "event") == event {
				return m
			}
		case <-timer.C:
			t.Fatalf("timed out awaiting Fake Node %s", event)
		}
	}
}
func pairChild(t *testing.T, f *fixture, user string, child *childNode) M {
	t.Helper()
	pair := child.wait(t, "pairing")
	preview := f.call("POST", "/v1/pairings/preview", user, "", M{"code": str(pair, "code")}, 200)
	f.call("POST", "/v1/pairings/confirm", user, id("operation"), M{"ticketId": str(preview, "ticketId")}, 200)
	connected := child.wait(t, "connected")
	waitNodeOnline(t, f, user, str(connected, "nodeId"))
	return connected
}
func waitNodeOnline(t *testing.T, f *fixture, user, nid string) {
	t.Helper()
	for i := 0; i < 30; i++ {
		node := f.call("GET", "/v1/nodes/"+nid, user, "", nil, 200)
		if truth(node, "online") {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("Node did not become routable")
}
func waitOperation(t *testing.T, f *fixture, user, op, state string) M {
	t.Helper()
	for i := 0; i < 40; i++ {
		m := f.call("GET", "/v1/operations/"+op, user, "", nil, 200)
		if str(m, "state") == state {
			return m
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("operation did not reach " + state)
	return nil
}

func TestIndependentFakeNodePersistsJournalAcrossRestart(t *testing.T) {
	f := newFixture(t)
	user := f.login("child-owner")
	dir := t.TempDir()
	child := startFakeChild(t, f.http.URL, dir, false)
	connected := pairChild(t, f, user, child)
	body := M{"nodeId": connected["nodeId"], "projectId": connected["projectId"], "agentId": connected["agentId"], "prompt": "完成协议测试", "capabilityRevision": 1}
	key := id("operation")
	op := f.call("POST", "/v1/sessions", user, key, body, 202)
	completed := child.wait(t, "turn.completed")
	receipt := waitOperation(t, f, user, str(op, "id"), "confirmed")
	sid := str(object(receipt, "result"), "sessionId")
	if sid != str(completed, "sessionId") {
		t.Fatal("wrong child session")
	}
	for i := 0; i < 20; i++ {
		session := f.call("GET", "/v1/sessions/"+sid, user, "", nil, 200)
		if str(session, "state") == "completed" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if e := child.cmd.Process.Kill(); e != nil {
		t.Fatal(e)
	}
	restarted := startFakeChild(t, f.http.URL, dir, false)
	next := restarted.wait(t, "connected")
	if str(next, "nodeId") != str(connected, "nodeId") {
		t.Fatal("identity changed across process restart")
	}
	waitNodeOnline(t, f, user, str(next, "nodeId"))
	same := f.call("POST", "/v1/sessions", user, key, body, 202)
	if str(same, "id") != str(op, "id") {
		t.Fatal("duplicate command changed operation")
	}
	if e := restarted.cmd.Process.Kill(); e != nil {
		t.Fatal(e)
	}
	// Reading synthetic private fixture locally is intentional; never print or commit it.
	b, e := os.ReadFile(filepath.Join(dir, "state.json"))
	if e != nil {
		t.Fatal(e)
	}
	var state fakenode.State
	if json.Unmarshal(b, &state) != nil {
		t.Fatal("journal corrupt after restart")
	}
	if state.NativeCalls != 1 || len(state.Journal) != 1 {
		t.Fatal("native work was replayed")
	}
}

func TestIndependentFakeNodeCrashWindowStaysUnknown(t *testing.T) {
	f := newFixture(t)
	user := f.login("crash-owner")
	dir := t.TempDir()
	child := startFakeChild(t, f.http.URL, dir, true)
	connected := pairChild(t, f, user, child)
	op := f.call("POST", "/v1/sessions", user, id("operation"), M{"nodeId": connected["nodeId"], "projectId": connected["projectId"], "agentId": connected["agentId"], "prompt": "simulate native acceptance crash", "capabilityRevision": 1}, 202)
	child.wait(t, "fault")
	if _, e := f.db.Exec(context.Background(), `UPDATE operations SET deadline_at=now()-interval '1 second' WHERE id=$1`, str(op, "id")); e != nil {
		t.Fatal(e)
	}
	f.app.mu.Lock()
	e := f.app.expireOperations(context.Background())
	f.app.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	restarted := startFakeChild(t, f.http.URL, dir, false)
	restarted.wait(t, "connected")
	waitOperation(t, f, user, str(op, "id"), "reconciling")
	if e := restarted.cmd.Process.Kill(); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(filepath.Join(dir, "state.json"))
	if e != nil {
		t.Fatal(e)
	}
	var state fakenode.State
	if json.Unmarshal(b, &state) != nil {
		t.Fatal("journal decode")
	}
	if state.NativeCalls != 1 || state.Journal[str(op, "id")].State != "unknown" {
		t.Fatal("ambiguous native acceptance was replayed or fabricated")
	}
}
