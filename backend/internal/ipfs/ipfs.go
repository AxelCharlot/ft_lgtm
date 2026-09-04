// Package ipfs shares one run through the Kubo RPC API: it adds the source and
// the output as one directory, pins them, and announces the directory to the
// DHT.
//
// The RPC port can write, so it never leaves the cluster. See k8s/README.md
// sections 2 and 5 for the address this client is given.
package ipfs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// SourceName and OutputName are the two files the directory holds. They are
	// flat on purpose: a part named "run/main.rs" makes Kubo build a "run"
	// directory and then wrap *that*, so the link would open a folder holding one
	// folder. The issue asks for a folder holding the two files.
	SourceName = "main.rs"
	OutputName = "output.txt"

	// DefaultAddTimeout bounds the add. The two files are a few kilobytes and the
	// node is one network hop away, so this is generous.
	DefaultAddTimeout = 10 * time.Second

	// DefaultProvideTimeout bounds the announce. Measured against the cluster on
	// 2026-08-29, one provide answered in 940 ms; this leaves room for a slow DHT
	// without letting one hold a user's request.
	DefaultProvideTimeout = 5 * time.Second
)

// Result is one shared run.
type Result struct {
	// CID is the directory, not either file.
	CID string

	// Link is what the browser opens. The backend composes it, because the page
	// is static and cannot read the address of the gateway from anywhere.
	Link string

	// Duration covers the add and the announce together, which is what the user
	// waited for.
	Duration time.Duration
}

// Client talks to one Kubo node.
type Client struct {
	// APIURL is the RPC address, IPFS_API_URL. No trailing slash is needed.
	APIURL string

	// GatewayURL is the public address of the gateway, IPFS_GATEWAY_URL. It is a
	// name from the hosts file of the machine that browses, not a Service name:
	// the browser runs outside the cluster.
	GatewayURL string

	// HTTPClient is optional. Zero means http.DefaultClient.
	HTTPClient *http.Client

	// AddTimeout and ProvideTimeout are optional. Zero means the defaults above.
	AddTimeout     time.Duration
	ProvideTimeout time.Duration
}

// addedEntry is one line of the answer of /api/v0/add. Kubo sends one JSON
// object per entry, newline separated, and the last one is the directory that
// wraps the rest.
type addedEntry struct {
	Name string `json:"Name"`
	Hash string `json:"Hash"`
}

// Upload adds the two files as one directory, pins it, and announces it.
//
// It returns an error when the run could not be shared. The caller decides what
// that means: the contract says a failed upload leaves the cid empty and sets no
// error, because the run itself succeeded and only the sharing did not.
func (c *Client) Upload(ctx context.Context, source, output string) (*Result, error) {
	started := time.Now()

	cid, err := c.add(ctx, source, output)
	if err != nil {
		return nil, err
	}

	// The announce is deliberately not fatal. The directory is added and pinned
	// by now, so the link already works on our own gateway; announcing only
	// decides how fast a *public* gateway can find it. Failing the whole upload
	// here would throw away a link that works.
	if err := c.provide(ctx, cid); err != nil {
		return &Result{
			CID:      cid,
			Link:     c.link(cid),
			Duration: time.Since(started),
		}, fmt.Errorf("the directory %s was added but not announced: %w", cid, err)
	}

	return &Result{
		CID:      cid,
		Link:     c.link(cid),
		Duration: time.Since(started),
	}, nil
}

// add posts both files in one multipart request and returns the directory CID.
func (c *Client) add(ctx context.Context, source, output string) (string, error) {
	body, contentType, err := multipartBody(source, output)
	if err != nil {
		return "", err
	}

	// wrap-with-directory turns the two files into one directory, which is what
	// the subject asks the link to open. pin keeps them once the run is over.
	query := url.Values{
		"wrap-with-directory": {"true"},
		"pin":                 {"true"},
	}

	response, err := c.post(ctx, "/api/v0/add", query, c.addTimeout(), body, contentType)
	if err != nil {
		return "", err
	}
	defer response.Close()

	return lastDirectory(response)
}

// provide announces the directory to the DHT at once, instead of waiting for the
// next reprovide round, which is far too slow to show to anyone.
func (c *Client) provide(ctx context.Context, cid string) error {
	query := url.Values{"arg": {cid}}

	response, err := c.post(ctx, "/api/v0/routing/provide", query, c.provideTimeout(), nil, "")
	if err != nil {
		return err
	}
	defer response.Close()

	// The answer is a stream of progress objects. Nothing in it is needed, but it
	// has to be drained before the connection can be reused.
	if _, err := io.Copy(io.Discard, response); err != nil {
		return fmt.Errorf("could not read the answer of the announce: %w", err)
	}
	return nil
}

// post sends one RPC call. Every call of this API is a POST, including the ones
// that only read.
func (c *Client) post(ctx context.Context, path string, query url.Values,
	timeout time.Duration, body io.Reader, contentType string) (io.ReadCloser, error) {
	deadline, cancel := context.WithTimeout(ctx, timeout)

	address := strings.TrimSuffix(c.APIURL, "/") + path + "?" + query.Encode()
	request, err := http.NewRequestWithContext(deadline, http.MethodPost, address, body)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("could not build the request for %s: %w", path, err)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}

	response, err := c.httpClient().Do(request)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("could not reach the IPFS node at %s: %w", path, err)
	}

	if response.StatusCode != http.StatusOK {
		// The body of a Kubo error is a JSON object with a Message. Reading a
		// bounded prefix of it turns "500" into a sentence a person can act on.
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		response.Body.Close()
		cancel()
		return nil, fmt.Errorf("the IPFS node answered %s on %s: %s",
			response.Status, path, strings.TrimSpace(string(detail)))
	}

	return &cancelOnClose{ReadCloser: response.Body, cancel: cancel}, nil
}

// multipartBody builds the two parts. Both are sent in one request, because two
// requests would give two directories and no way to join them.
func multipartBody(source, output string) (io.Reader, string, error) {
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)

	for _, file := range []struct{ name, content string }{
		{SourceName, source},
		{OutputName, output},
	} {
		part, err := writer.CreateFormFile("file", file.name)
		if err != nil {
			return nil, "", fmt.Errorf("could not build the part %s: %w", file.name, err)
		}
		if _, err := io.WriteString(part, file.content); err != nil {
			return nil, "", fmt.Errorf("could not write the part %s: %w", file.name, err)
		}
	}

	if err := writer.Close(); err != nil {
		return nil, "", fmt.Errorf("could not close the multipart body: %w", err)
	}
	return &buffer, writer.FormDataContentType(), nil
}

// lastDirectory reads the newline separated answer of add and returns the hash
// of the last entry.
//
// The order is the contract of the API, not a guess: Kubo sends each file first
// and the directory that wraps them last. Reading the entry whose Name is empty
// would work today and break the day a file is added with no name.
func lastDirectory(body io.Reader) (string, error) {
	decoder := json.NewDecoder(body)

	var last addedEntry
	for {
		var entry addedEntry
		err := decoder.Decode(&entry)
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("could not read the answer of the add: %w", err)
		}
		last = entry
	}

	if last.Hash == "" {
		return "", fmt.Errorf("the IPFS node added nothing")
	}
	return last.Hash, nil
}

// link builds what the browser opens. The subject prints this address with no
// port, which is why section 3 of the contract gives the machine an address of
// its own.
func (c *Client) link(cid string) string {
	return strings.TrimSuffix(c.GatewayURL, "/") + "/ipfs/" + cid
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

func (c *Client) addTimeout() time.Duration {
	if c.AddTimeout <= 0 {
		return DefaultAddTimeout
	}
	return c.AddTimeout
}

func (c *Client) provideTimeout() time.Duration {
	if c.ProvideTimeout <= 0 {
		return DefaultProvideTimeout
	}
	return c.ProvideTimeout
}

// cancelOnClose releases the context of a call when its body is closed. Without
// it the deadline of every request would leak until it expired on its own.
type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}
