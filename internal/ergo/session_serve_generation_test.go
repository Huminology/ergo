package ergo

import (
	"encoding/json"
	"net"
	"sync"
	"testing"
)

func TestSessionGenerationStartsAtOneAndBumpsOnCreate(t *testing.T) {
	dir := t.TempDir()
	if _, err := InitializeRepository(dir); err != nil {
		t.Fatal(err)
	}
	app := NewApplication(RepositoryOptions{StartDir: dir})
	session, err := app.OpenSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	n, err := session.Generation()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("generation = %d, want 1", n)
	}
	if _, err := session.CreateTask(CreateTaskRequest{Title: "Warm"}); err != nil {
		t.Fatal(err)
	}
	after, err := session.Generation()
	if err != nil {
		t.Fatal(err)
	}
	if after <= n {
		t.Fatalf("generation after create = %d, want > %d", after, n)
	}
}

func TestServeConcurrentJSONLists(t *testing.T) {
	dir := t.TempDir()
	if _, err := InitializeRepository(dir); err != nil {
		t.Fatal(err)
	}
	app := NewApplication(RepositoryOptions{StartDir: dir})
	if _, err := app.CreateTask(CreateTaskRequest{Title: "Task", Body: "body\n"}); err != nil {
		t.Fatal(err)
	}
	session, err := app.OpenSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- serveListJSON(session, ListRequest{OmitJournal: true, ShowAll: true})
		}()
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- serveListJSON(session, ListRequest{
				OmitJournal: true, ReadyOnly: true, JSONWithMeta: true, JSONWithBody: true,
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestServeWatchReceivesGenerationAfterMutation(t *testing.T) {
	dir := t.TempDir()
	if _, err := InitializeRepository(dir); err != nil {
		t.Fatal(err)
	}
	app := NewApplication(RepositoryOptions{StartDir: dir})
	session, err := app.OpenSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	server, client := net.Pipe()
	defer client.Close()
	go ServeSession(session, server)

	if err := writeServeRequest(client, session.ProjectDir(), "watch", struct{}{}); err != nil {
		t.Fatal(err)
	}
	first, err := readWireResponse(client)
	if err != nil {
		t.Fatal(err)
	}
	if !first.OK {
		t.Fatalf("watch start: %+v", first)
	}
	before := parseGenerationJSON(t, first.Stdout)

	if _, err := session.CreateTask(CreateTaskRequest{Title: "Watched"}); err != nil {
		t.Fatal(err)
	}
	second, err := readWireResponse(client)
	if err != nil {
		t.Fatal(err)
	}
	if !second.OK {
		t.Fatalf("watch update: %+v", second)
	}
	after := parseGenerationJSON(t, second.Stdout)
	if after <= before {
		t.Fatalf("watched generation %d, want > %d", after, before)
	}
}

func TestServeGenerationMethod(t *testing.T) {
	dir := t.TempDir()
	if _, err := InitializeRepository(dir); err != nil {
		t.Fatal(err)
	}
	app := NewApplication(RepositoryOptions{StartDir: dir})
	session, err := app.OpenSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	server, client := net.Pipe()
	defer client.Close()
	go ServeSession(session, server)
	if err := writeServeRequest(client, session.ProjectDir(), "generation", struct{}{}); err != nil {
		t.Fatal(err)
	}
	resp, err := readWireResponse(client)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.OK {
		t.Fatalf("generation: %+v", resp)
	}
	if parseGenerationJSON(t, resp.Stdout) != 1 {
		t.Fatalf("stdout = %s", resp.Stdout)
	}
}

func serveListJSON(session *Session, req ListRequest) error {
	server, client := net.Pipe()
	defer client.Close()
	go ServeSession(session, server)
	if err := writeServeRequest(client, session.ProjectDir(), "list", req); err != nil {
		return err
	}
	resp, err := readWireResponse(client)
	if err != nil {
		return err
	}
	if !resp.OK {
		return errString("list failed: " + resp.Stderr)
	}
	var document struct {
		Version    int   `json:"version"`
		Generation uint64 `json:"generation"`
		Items      []any `json:"items"`
	}
	if err := json.Unmarshal([]byte(resp.Stdout), &document); err != nil {
		return err
	}
	if document.Generation < 1 {
		return errString("missing generation")
	}
	return nil
}

func writeServeRequest(conn net.Conn, dir, method string, payload any) error {
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(WireRequest{
		V: protocolVersion, Method: method, Dir: dir, Payload: rawPayload,
	})
	if err != nil {
		return err
	}
	return writeWirePayload(conn, raw)
}

func parseGenerationJSON(t *testing.T, raw string) uint64 {
	t.Helper()
	var document struct {
		Generation uint64 `json:"generation"`
	}
	if err := json.Unmarshal([]byte(raw), &document); err != nil {
		t.Fatalf("generation json %q: %v", raw, err)
	}
	return document.Generation
}

type errString string

func (e errString) Error() string { return string(e) }
