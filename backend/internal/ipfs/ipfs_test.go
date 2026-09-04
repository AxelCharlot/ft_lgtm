package ipfs

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// addAnswer is the shape Kubo really sends: one JSON object per entry, newline
// separated, the wrapping directory last. Copied from a live node on
// 2026-08-29.
const addAnswer = `{"Name":"main.rs","Hash":"QmFileOne","Size":"38"}
{"Name":"output.txt","Hash":"QmFileTwo","Size":"11"}
{"Name":"","Hash":"QmDirectory","Size":"154"}
`

// nodeCall is one request the fake node received.
type nodeCall struct {
	path        string
	query       url.Values
	contentType string
	body        string
}

// fakeNode answers like Kubo and records what it was asked. addStatus and
// provideStatus let a test break one call and leave the other working.
type fakeNode struct {
	addStatus     int
	provideStatus int
	addBody       string
	calls         []nodeCall
}

func (n *fakeNode) start(t *testing.T) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("the fake node could not read the body: %v", err)
		}
		n.calls = append(n.calls, nodeCall{
			path:        r.URL.Path,
			query:       r.URL.Query(),
			contentType: r.Header.Get("Content-Type"),
			body:        string(body),
		})

		if r.Method != http.MethodPost {
			t.Errorf("the node was called with %s, want POST", r.Method)
		}

		switch r.URL.Path {
		case "/api/v0/add":
			w.WriteHeader(n.addStatus)
			fmt.Fprint(w, n.addBody)
		case "/api/v0/routing/provide":
			w.WriteHeader(n.provideStatus)
		default:
			t.Errorf("the node was called on %s, which this client never uses", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func workingNode() *fakeNode {
	return &fakeNode{addStatus: http.StatusOK, provideStatus: http.StatusOK, addBody: addAnswer}
}

func clientFor(server *httptest.Server) *Client {
	return &Client{APIURL: server.URL, GatewayURL: "http://ipfs.lgtm.local"}
}

func TestUploadReturnsTheDirectoryAndItsLink(t *testing.T) {
	node := workingNode()
	client := clientFor(node.start(t))

	result, err := client.Upload(context.Background(), "fn main() {}", "hello\n")
	if err != nil {
		t.Fatalf("Upload returned %v, want no error", err)
	}

	// The last entry, not the first: the first two are the files.
	if result.CID != "QmDirectory" {
		t.Errorf("CID is %q, want the last entry QmDirectory", result.CID)
	}
	if result.Link != "http://ipfs.lgtm.local/ipfs/QmDirectory" {
		t.Errorf("Link is %q", result.Link)
	}
	if result.Duration <= 0 {
		t.Error("Duration is zero, so nothing was measured")
	}
}

func TestUploadSendsBothFilesInOneRequest(t *testing.T) {
	node := workingNode()
	client := clientFor(node.start(t))

	if _, err := client.Upload(context.Background(), "fn main() {}", "hello\n"); err != nil {
		t.Fatalf("Upload returned %v", err)
	}

	if len(node.calls) != 2 {
		t.Fatalf("the node saw %d calls, want the add then the announce", len(node.calls))
	}

	add := node.calls[0]
	if add.path != "/api/v0/add" {
		t.Errorf("the first call is %s, want /api/v0/add", add.path)
	}
	if !strings.HasPrefix(add.contentType, "multipart/form-data") {
		t.Errorf("the add was sent as %q, want multipart/form-data", add.contentType)
	}

	// Both files travel in one request: two requests would build two directories
	// with no way to join them.
	for _, want := range []string{`filename="main.rs"`, `filename="output.txt"`} {
		if !strings.Contains(add.body, want) {
			t.Errorf("the body has no part with %s", want)
		}
	}
	for _, want := range []string{"fn main() {}", "hello\n"} {
		if !strings.Contains(add.body, want) {
			t.Errorf("the body does not carry %q", want)
		}
	}

	// A part named run/main.rs would make Kubo wrap a directory inside a
	// directory, and the link would open a folder holding one folder.
	if strings.Contains(add.body, "run/") {
		t.Error("a part is named with a run/ prefix, which wraps the files twice")
	}
}

func TestUploadAsksForOneDirectoryAndAPin(t *testing.T) {
	node := workingNode()
	client := clientFor(node.start(t))

	if _, err := client.Upload(context.Background(), "fn main() {}", "hello\n"); err != nil {
		t.Fatalf("Upload returned %v", err)
	}

	query := node.calls[0].query
	if got := query.Get("wrap-with-directory"); got != "true" {
		t.Errorf("wrap-with-directory is %q, so the link would open a file", got)
	}
	if got := query.Get("pin"); got != "true" {
		t.Errorf("pin is %q, so the run could be swept away", got)
	}
}

func TestUploadAnnouncesTheDirectory(t *testing.T) {
	node := workingNode()
	client := clientFor(node.start(t))

	if _, err := client.Upload(context.Background(), "fn main() {}", "hello\n"); err != nil {
		t.Fatalf("Upload returned %v", err)
	}

	announce := node.calls[1]
	if announce.path != "/api/v0/routing/provide" {
		t.Fatalf("the second call is %s, want the announce", announce.path)
	}
	// Without it the CID waits for the next reprovide round, which no
	// demonstration has time for.
	if got := announce.query.Get("arg"); got != "QmDirectory" {
		t.Errorf("the announce carries %q, want the directory", got)
	}
}

func TestUploadKeepsTheLinkWhenTheAnnounceFails(t *testing.T) {
	node := workingNode()
	node.provideStatus = http.StatusInternalServerError
	client := clientFor(node.start(t))

	result, err := client.Upload(context.Background(), "fn main() {}", "hello\n")
	if err == nil {
		t.Fatal("Upload returned no error, and the announce failed")
	}

	// The directory is added and pinned by then, so our own gateway already
	// serves it. Only a public gateway is slower to find it.
	if result == nil {
		t.Fatal("Upload returned no result, and the link already worked")
	}
	if result.CID != "QmDirectory" || result.Link == "" {
		t.Errorf("the result lost the link: %+v", result)
	}
}

func TestUploadFailsWhenTheNodeRefusesTheAdd(t *testing.T) {
	node := workingNode()
	node.addStatus = http.StatusInternalServerError
	node.addBody = `{"Message":"repo is full","Code":0}`
	client := clientFor(node.start(t))

	result, err := client.Upload(context.Background(), "fn main() {}", "hello\n")
	if err == nil {
		t.Fatal("Upload returned no error, and the node refused")
	}
	if result != nil {
		t.Errorf("Upload returned %+v, want nothing", result)
	}
	// The message of the node reaches the log, or a 500 says nothing at all.
	if !strings.Contains(err.Error(), "repo is full") {
		t.Errorf("the error is %q and does not carry what the node said", err)
	}
}

func TestUploadFailsWhenTheNodeAddsNothing(t *testing.T) {
	node := workingNode()
	node.addBody = ""
	client := clientFor(node.start(t))

	if _, err := client.Upload(context.Background(), "fn main() {}", "hello\n"); err == nil {
		t.Fatal("Upload returned no error, and the node added nothing")
	}
}

func TestLinkJoinsWithOneSlash(t *testing.T) {
	client := &Client{GatewayURL: "http://ipfs.lgtm.local/"}
	if got := client.link("QmDirectory"); got != "http://ipfs.lgtm.local/ipfs/QmDirectory" {
		t.Errorf("link is %q, and a doubled slash breaks the gateway", got)
	}
}
