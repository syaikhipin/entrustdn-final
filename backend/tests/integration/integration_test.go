// Package integration runs the one thin Seam 2 suite the spec agreed: real
// backend and real agent processes over the wire, proving the ticket 11
// demo — chat with a Member over a Channel, kill the agent process,
// restart it, reply days-later-style, and the conversation resumes
// mid-thread. No test touches live WhatsApp/Telegram/STT/LLM APIs: the
// Member's channel is email, whose dev medium is the agent's log sink, and
// the LangGraph checkpoint rides the real Postgres saver (production
// configuration, THRESH_CHECKPOINT_URL).
//
// The suite owns its own database (THRESH_INTEGRATION_DATABASE_URL) so
// parallel `go test ./...` package truncations cannot wipe it mid-run, and
// skips unless that URL is set — the same gate TEST_DATABASE_URL uses.
package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// repoRoot locates the monorepo root from this file's own path.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test file")
	}
	root, err := filepath.Abs(filepath.Join(filepath.Dir(thisFile), "..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	return root
}

// freePort binds :0, reads the port, and gives it away — the usual
// small-race-but-fine test pattern for pinning a child process's port.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find a free port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// resetDatabase drops and recreates the public schema so both processes
// boot onto a pristine database (the compose volume persists between runs).
func resetDatabase(t *testing.T, url string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect integration database: %v", err)
	}
	defer conn.Close(context.Background())
	for _, stmt := range []string{
		`DROP SCHEMA public CASCADE`,
		`CREATE SCHEMA public`,
	} {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
}

// process is one spawned child with its log captured to a file for
// debugging and (for the backend) link fishing.
type process struct {
	cmd  *exec.Cmd
	log  *os.File
	name string
}

func startProcess(t *testing.T, name, dir string, argv []string, extraEnv ...string) *process {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), name+".log")
	log, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("open %s log: %v", name, err)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), extraEnv...)
	// Its own process group: `uv run` execs through wrapper processes, so
	// killing the bare PID would leave the real server listening.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout = log
	cmd.Stderr = log
	if err := cmd.Start(); err != nil {
		log.Close()
		t.Fatalf("start %s: %v", name, err)
	}
	p := &process{cmd: cmd, log: log, name: name}
	t.Cleanup(func() {
		_ = p.killGroup()
		_, _ = p.cmd.Process.Wait()
		_ = p.log.Close()
	})
	return p
}

// killGroup SIGKILLs the process's whole group — wrappers and all.
func (p *process) killGroup() error {
	return syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
}

// die kills the process abruptly — SIGKILL to the whole group, the
// "crashed" the demo means.
func (p *process) die(t *testing.T) {
	t.Helper()
	if err := p.killGroup(); err != nil {
		t.Fatalf("kill %s: %v", p.name, err)
	}
	_, _ = p.cmd.Process.Wait()
	_ = p.log.Close()
}

// logText reads everything the process has written so far.
func (p *process) logText(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(p.log.Name())
	if err != nil {
		t.Fatalf("read %s log: %v", p.name, err)
	}
	return string(raw)
}

// client is a tiny JSON HTTP client against one base URL.
type client struct {
	base string
}

func (c client) do(t *testing.T, method, path, token string, body any) (int, map[string]any) {
	t.Helper()
	code, doc, err := c.tryDo(method, path, token, body)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return code, doc
}

// tryDo is do without the fatal: callers polling a booting process need
// connection refused to be a retryable condition, not a test failure.
func (c client) tryDo(method, path, token string, body any) (int, map[string]any, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.base+path, reader)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var doc map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &doc); err != nil {
			return resp.StatusCode, nil, fmt.Errorf("%s %s: response is not JSON: %s", method, path, raw)
		}
	}
	return resp.StatusCode, doc, nil
}

// pollUntil hits the endpoint until want says so or the deadline passes.
// Connection errors are retryable — the child process may still be booting.
func pollUntil(t *testing.T, what string, c client, method, path, token string, body any, want func(code int, doc map[string]any) bool) (int, map[string]any) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	var lastCode, lastDoc = 0, map[string]any(nil)
	for {
		code, doc, err := c.tryDo(method, path, token, body)
		if err == nil {
			lastCode, lastDoc = code, doc
			if want(code, doc) {
				return code, doc
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: never settled (last: %d %v %v)", what, lastCode, lastDoc, err)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// TestConversationSurvivesAgentRestartAndResumes is the ticket's demo:
// real processes, real Postgres checkpoints, a killed agent, and a Member
// who answers days later. The conversation resumes mid-thread.
func TestConversationSurvivesAgentRestartAndResumes(t *testing.T) {
	dbURL := os.Getenv("THRESH_INTEGRATION_DATABASE_URL")
	if dbURL == "" {
		t.Skip("THRESH_INTEGRATION_DATABASE_URL not set — the thin integration suite needs its own database")
	}
	root := repoRoot(t)
	resetDatabase(t, dbURL)

	backendPort := freePort(t)
	agentPort := freePort(t)
	backend := client{base: fmt.Sprintf("http://127.0.0.1:%d", backendPort)}

	// One checkpoint database serves every agent process this test spawns:
	// that is the whole point — the checkpoint outlives any single process.
	startAgent := func() *process {
		return startProcess(t, "agent-"+fmt.Sprint(freePort(t)), filepath.Join(root, "agent"),
			[]string{"uv", "run", "python", "-m", "thresh_agent"},
			"AGENT_HOST=127.0.0.1",
			"AGENT_PORT="+fmt.Sprint(agentPort),
			"THRESH_CHECKPOINT_URL="+dbURL,
		)
	}
	backendProc := startProcess(t, "backend", filepath.Join(root, "backend"),
		[]string{"go", "run", "./cmd/api"},
		"DATABASE_URL="+dbURL,
		"AGENT_BASE_URL=http://127.0.0.1:"+fmt.Sprint(agentPort),
		"BACKEND_ADDR=127.0.0.1:"+fmt.Sprint(backendPort),
		"BOOTSTRAP_ADMIN_EMAIL=admin@thresh.dev",
		"BOOTSTRAP_ADMIN_PASSWORD=integration-admin-2026",
		"THRESH_PUBLIC_BASE_URL=http://localhost:3000",
	)
	agentProc := startAgent()

	// Both processes are up and talking to each other.
	pollUntil(t, "backend status", backend, http.MethodGet, "/api/v1/status", "", nil,
		func(code int, doc map[string]any) bool {
			agent, ok := doc["agent"].(map[string]any)
			return code == http.StatusOK && ok && agent["reachable"] == true
		})

	// The org signs up, verifies (the dev log sink carried the link),
	// gets approved by the bootstrapped admin, and logs in.
	orgEmail := "co-op@integration.example.org"
	code, doc := backend.do(t, http.MethodPost, "/api/v1/register", "", map[string]any{
		"email": orgEmail, "password": "harvest-2026", "display_name": "Integration Co-op",
		"role": "farmer_organization", "tos_version": "1.0",
	})
	if code != http.StatusCreated {
		t.Fatalf("register org = %d (%v)", code, doc)
	}
	orgID := doc["account"].(map[string]any)["id"].(string)
	verifyToken := fishVerificationToken(t, backendProc, orgEmail)
	code, doc = backend.do(t, http.MethodPost, "/api/v1/verify", "", map[string]any{"token": verifyToken})
	if code != http.StatusOK {
		t.Fatalf("verify = %d (%v)", code, doc)
	}
	_, adminLogin := backend.do(t, http.MethodPost, "/api/v1/login", "", map[string]any{
		"email": "admin@thresh.dev", "password": "integration-admin-2026",
	})
	adminToken := adminLogin["session"].(map[string]any)["token"].(string)
	if dcode, ddoc := backend.do(t, http.MethodPost, "/api/v1/admin/applications/decide", adminToken, map[string]any{
		"account_id": orgID, "decision": "approve",
	}); dcode != http.StatusOK {
		t.Fatalf("approve org = %d (%v)", dcode, ddoc)
	}
	_, orgLogin := backend.do(t, http.MethodPost, "/api/v1/login", "", map[string]any{
		"email": orgEmail, "password": "harvest-2026",
	})
	orgToken := orgLogin["session"].(map[string]any)["token"].(string)

	// Roster one Member on email — the channel whose dev medium is the
	// agent's log sink. No accounts: a contact point is the whole Member.
	code, doc = backend.do(t, http.MethodPost, "/api/v1/members", orgToken, map[string]any{
		"display_name": "Siobhán", "contact": "email:siobhan@farm.ie",
	})
	if code != http.StatusCreated {
		t.Fatalf("add member = %d (%v)", code, doc)
	}
	memberID := doc["member"].(map[string]any)["id"].(string)

	// Open the conversation: two questions, agent asks the first.
	questions := []string{"What crop did you plant this season?", "How many hectares are under it?"}
	code, doc = backend.do(t, http.MethodPost, "/api/v1/conversations", orgToken, map[string]any{
		"member_id": memberID, "topic": "this season's cropping", "questions": questions,
	})
	if code != http.StatusCreated {
		t.Fatalf("open conversation = %d (%v)", code, doc)
	}
	conv := doc["conversation"].(map[string]any)
	token := conv["resume_token"].(string)
	thread := conv["thread"].([]any)
	if len(thread) != 1 {
		t.Fatalf("thread = %v, want the agent's opening question", thread)
	}
	// The resumable link rode the opening message — the Member's capability.
	if !strings.Contains(thread[0].(map[string]any)["body"].(string), token) {
		t.Errorf("opening message does not carry the resumable token:\n%v", thread[0])
	}

	// The agent dies. The Member's reply can't be taken — and the backend
	// says so honestly (502) while the durable record keeps its state.
	agentProc.die(t)
	code, doc = backend.do(t, http.MethodPost, "/api/v1/member/reply", "", map[string]any{
		"token": token, "message": "Spring barley",
	})
	if code != http.StatusBadGateway {
		t.Fatalf("reply with the agent down = %d (%v), want 502", code, doc)
	}
	if code, doc := backend.do(t, http.MethodGet, "/api/v1/conversations/"+conv["id"].(string), orgToken, nil); code != http.StatusOK ||
		doc["conversation"].(map[string]any)["status"] != "awaiting_member" {
		t.Fatalf("conversation did not survive the outage: %d (%v)", code, doc)
	}

	// A new agent process boots on the same checkpoint database — the
	// restart the demo names. Days may pass; nothing in the design cares.
	agentProc = startAgent()
	pollUntil(t, "agent restart", backend, http.MethodGet, "/api/v1/status", "", nil,
		func(code int, doc map[string]any) bool {
			agent, ok := doc["agent"].(map[string]any)
			return code == http.StatusOK && ok && agent["reachable"] == true
		})

	// The Member replies — the conversation resumes mid-thread.
	code, doc = backend.do(t, http.MethodPost, "/api/v1/member/reply", "", map[string]any{
		"token": token, "message": "Spring barley",
	})
	if code != http.StatusOK {
		t.Fatalf("resumed reply = %d (%v)", code, doc)
	}
	conv = doc["conversation"].(map[string]any)
	if conv["status"] != "awaiting_member" {
		t.Errorf("status after resumed reply = %v, want awaiting_member (one question left)", conv["status"])
	}
	thread = conv["thread"].([]any)
	if len(thread) != 3 {
		t.Fatalf("thread = %v, want ask → reply → next ask", thread)
	}
	if got := thread[1].(map[string]any)["body"]; got != "Spring barley" {
		t.Errorf("member turn = %v, want the Member's answer", got)
	}
	if got := thread[2].(map[string]any)["body"]; got != questions[1] {
		t.Errorf("next question = %v, want %q — pacing resumed where it paused", got, questions[1])
	}

	// The last answer completes the survey.
	code, doc = backend.do(t, http.MethodPost, "/api/v1/member/reply", "", map[string]any{
		"token": token, "message": "Twelve hectares",
	})
	if code != http.StatusOK {
		t.Fatalf("final reply = %d (%v)", code, doc)
	}
	conv = doc["conversation"].(map[string]any)
	if conv["status"] != "completed" {
		t.Errorf("status after final reply = %v, want completed", conv["status"])
	}
	answers := conv["answers"].([]any)
	if len(answers) != 2 || answers[0] != "Spring barley" || answers[1] != "Twelve hectares" {
		t.Errorf("answers = %v, want both answers in order", answers)
	}

	// The org's view of the finished conversation agrees with the record.
	code, _ = backend.do(t, http.MethodGet, "/api/v1/conversations/"+conv["id"].(string), orgToken, nil)
	if code != http.StatusOK {
		t.Fatalf("org view = %d (%v)", code, doc)
	}
	if got := doc["conversation"].(map[string]any)["status"]; got != "completed" {
		t.Errorf("org view status = %v, want completed", got)
	}
}

// fishVerificationToken reads the backend's log until the dev mail sink
// has delivered the org's verification link, then returns its token.
func fishVerificationToken(t *testing.T, backend *process, email string) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	needle := "verification link for " + email + ": "
	for {
		for _, line := range strings.Split(backend.logText(t), "\n") {
			if i := strings.Index(line, needle); i >= 0 {
				link := strings.TrimSpace(line[i+len(needle):])
				if j := strings.Index(link, "token="); j >= 0 {
					return strings.TrimSpace(link[j+len("token="):])
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("verification link for %s never landed in the backend log:\n%s", email, backend.logText(t))
		}
		time.Sleep(200 * time.Millisecond)
	}
}
