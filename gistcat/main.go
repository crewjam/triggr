package main

import (
	"bytes"
	"context"
	"flag"
	"io"
	"os"
	"sync"
	"time"

	"github.com/google/go-github/github"
	"github.com/jpillora/backoff"
	"golang.org/x/oauth2"
)

var (
	githubAccessToken = flag.String("token", os.Getenv("GITHUB_ACCESS_TOKEN"), "The personal access token to manipulate github")
	gistID            = flag.String("gist", os.Getenv("GIST_ID"), "The ID of the gist")
	fileName          = flag.String("file-name", os.Getenv("GIST_FILE_NAME"), "The name of the file in the gist")
)

var githubClient *github.Client

func main() {
	flag.Parse()

	ctx := context.Background()
	githubClient = github.NewClient(
		oauth2.NewClient(ctx,
			oauth2.StaticTokenSource(
				&oauth2.Token{AccessToken: *githubAccessToken},
			),
		),
	)

	outputWriter := &GistWriter{
		Context:  ctx,
		ID:       *gistID,
		FileName: *fileName,
	}
	defer outputWriter.Close()

	stdin := io.TeeReader(os.Stdin, os.Stdout)
	io.Copy(outputWriter, stdin)
}

type GistWriter struct {
	Context       context.Context
	ID            string
	Description   string
	FileName      string
	mu            sync.Mutex
	lastFlushTime time.Time
	lastFlushSize int
	buf           *bytes.Buffer
}

func (gw *GistWriter) Write(buf []byte) (int, error) {
	gw.mu.Lock()
	defer gw.mu.Unlock()

	if gw.buf == nil {
		gw.buf = bytes.NewBuffer(nil)
	}
	gw.buf.Write(buf)

	const maxBytesSinceFlush = 10
	const maxTimeSinceFlush = 5 * time.Second
	if time.Since(gw.lastFlushTime) > maxTimeSinceFlush || gw.buf.Len()-gw.lastFlushSize > maxBytesSinceFlush {
		if err := gw.flush(); err != nil {
			return 0, err
		}
	}
	return len(buf), nil
}

func (gw *GistWriter) Flush() error {
	gw.mu.Lock()
	defer gw.mu.Unlock()
	return gw.flush()
}

func (gw *GistWriter) Close() error {
	gw.mu.Lock()
	defer gw.mu.Unlock()
	if err := gw.flush(); err != nil {
		return err
	}
	gw.buf = nil
	return nil
}

func (gw *GistWriter) flush() error {
	if gw.lastFlushSize == gw.buf.Len() {
		gw.lastFlushTime = time.Now()
		return nil
	}

	bo := backoff.Backoff{}
	for {
		_, resp, err := githubClient.Gists.Edit(gw.Context, gw.ID, &github.Gist{
			Description: github.String(gw.Description),
			Files: map[github.GistFilename]github.GistFile{
				github.GistFilename(gw.FileName): github.GistFile{
					Type:    github.String("text/plain"),
					Content: github.String(string(gw.buf.Bytes())),
				},
			},
		})
		if err != nil {
			if resp.StatusCode >= 500 {
				time.Sleep(bo.Duration())
				continue
			}
			return err
		}
		break
	}
	gw.lastFlushTime = time.Now()
	gw.lastFlushSize = gw.buf.Len()
	return nil
}
