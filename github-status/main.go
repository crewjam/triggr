package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/oauth2"

	"github.com/google/go-github/github"
)

var (
	token            = flag.String("token", os.Getenv("GITHUB_ACCESS_TOKEN"), "The personal access token to manipulate github")
	repoFullName     = flag.String("repo", "GITHUB_REPO", "The gitub repository name, e.g. alice/example")
	rev              = flag.String("rev", "GITHUB_REVISION", "The revision")
	targetURL        = flag.String("target-url", os.Getenv("GITHUB_TARGET_URL"), "Target URL")
	statusContext    = flag.String("context", os.Getenv("GITHUB_STATUS_CONTEXT"), "The name of this application, unique from others")
	setStatusPending = flag.Bool("set-pending", true, "Mark the job pending")
)

var githubClient *github.Client

func main() {

	flag.Parse()
	ctx := context.Background()
	githubClient = github.NewClient(
		oauth2.NewClient(ctx,
			oauth2.StaticTokenSource(
				&oauth2.Token{AccessToken: *token},
			),
		),
	)

	var owner, repoName string
	{
		parts := strings.Split(*repoFullName, "/")
		owner, repoName = parts[0], parts[1]
	}

	status := &github.RepoStatus{
		State:       github.String("pending"),
		TargetURL:   github.String(*targetURL),
		Description: github.String("started"),
		Context:     github.String(*statusContext),
	}
	if *setStatusPending {
		_, _, err := githubClient.Repositories.CreateStatus(ctx,
			owner,
			repoName,
			*rev,
			status,
		)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cannot set github status: %v\n", err)
			os.Exit(1)
		}
	}

	exitCode := 0
	cmd := exec.Command(flag.Args()[0], flag.Args()[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	if err := cmd.Start(); err != nil {
		status.State = github.String("error")
		status.Description = github.String(err.Error())
		exitCode = 1
	} else if err := cmd.Wait(); err != nil {
		status.State = github.String("failure")
		status.Description = github.String(err.Error())
		exitCode = 1
	} else {
		status.State = github.String("success")
		status.Description = github.String("success")
	}
	_, _, err := githubClient.Repositories.CreateStatus(ctx,
		owner,
		repoName,
		*rev,
		status,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot set github status: %v\n", err)
		os.Exit(1)
	}

	os.Exit(exitCode)
}
