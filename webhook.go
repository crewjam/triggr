package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"

	"github.com/BurntSushi/toml"
	"github.com/crewjam/httperr"
	"github.com/google/go-github/github"
	"github.com/kr/pretty"
	goji "goji.io"
	"goji.io/pat"
	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
)

func runServer() {
	if *listenAddress == "" {
		*listenAddress = ":8000"
	}
	mux := goji.NewMux()
	mux.Handle(pat.Post("/event"), httperr.HandlerFunc(handleEvent))
	http.ListenAndServe(*listenAddress, mux)
}

func handleEvent(w http.ResponseWriter, r *http.Request) error {
	payload, err := github.ValidatePayload(r, []byte(*githubWebhookSecret))
	if err != nil {
		log.Printf("ValidatePayload: %v", err)
		return err
	}
	event, err := github.ParseWebHook(github.WebHookType(r), payload)
	if err != nil {
		log.Printf("ParseWebHook: %v", err)
		return err
	}

	switch event := event.(type) {
	case *github.PullRequestEvent:
		err := handlePullRequest(r.Context(), event)
		if err != nil {
			log.Printf("handlePullRequest: %v", err)
		}
		return err
	}
	return nil
}

type Builder struct {
	Event     *github.PullRequestEvent
	Gist      *github.Gist
	Config    Config
	TargetURL string
}

type Config struct {
	Image string
	Tasks []TaskConfig `toml:"task"`
}

type TaskConfig struct {
	Name    string
	Image   string
	Command []string
}

func handlePullRequest(ctx context.Context, event *github.PullRequestEvent) error {
	b := Builder{
		Event: event,
		Gist: &github.Gist{
			Description: github.String(event.PullRequest.Base.Repo.GetFullName() + " Build Status"),
			Public:      github.Bool(false),
			Files:       map[github.GistFilename]github.GistFile{},
		},
	}
	if err := b.getConfig(ctx); err != nil {
		return err
	}
	if err := b.writeGist(ctx); err != nil {
		return err
	}

	for _, task := range b.Config.Tasks {
		if err := b.startTask(ctx, task); err != nil {
			return err
		}
	}
	return nil
}

func (b *Builder) getConfig(ctx context.Context) error {
	configFileContent, _, _, err := githubClient.Repositories.GetContents(ctx,
		b.Event.PullRequest.Base.Repo.Owner.GetLogin(),
		b.Event.PullRequest.Base.Repo.GetName(),
		".triggr.toml",
		&github.RepositoryContentGetOptions{
			Ref: b.Event.PullRequest.Head.GetSHA(),
		})
	if err != nil {
		return fmt.Errorf("cannot fetch .triggr.toml file: %v", err)
	}
	configBuf, err := configFileContent.GetContent()
	if err != nil {
		return fmt.Errorf("cannot parse .triggr.toml file: %v", err)
	}
	if _, err := toml.Decode(configBuf, &b.Config); err != nil {
		return fmt.Errorf("cannot parse .triggr.toml file TOML: %v", err)
	}
	return nil
}

func (b *Builder) writeGist(ctx context.Context) error {
	mdBuf := bytes.NewBuffer(nil)
	fmt.Fprintln(mdBuf, "# Build Record")
	fmt.Fprintln(mdBuf)
	fmt.Fprintf(mdBuf, "Repo: [%s](https://github.com/%s)\n",
		b.Event.PullRequest.Base.Repo.GetFullName(),
		b.Event.PullRequest.Base.Repo.GetFullName())
	fmt.Fprintln(mdBuf)
	fmt.Fprintf(mdBuf, "PR: [#%d %s](%s)\n",
		b.Event.PullRequest.GetNumber(),
		b.Event.PullRequest.GetTitle(),
		b.Event.PullRequest.GetHTMLURL())
	fmt.Fprintln(mdBuf)
	fmt.Fprintf(mdBuf, "Commit: [%s](%s)\n",
		b.Event.PullRequest.Head.GetSHA(),
		fmt.Sprintf("https://github.com/%s/commit/%s",
			b.Event.PullRequest.Head.Repo.GetFullName(),
			b.Event.PullRequest.Head.GetSHA()))
	fmt.Fprintln(mdBuf)
	pretty.Fprintf(mdBuf, "Hack:\n"+
		"```\n"+
		"%# v\n"+
		"```\n\n", b.Config)
	fmt.Fprintln(mdBuf)

	for _, task := range b.Config.Tasks {
		fmt.Fprintln(mdBuf, "## Task", task.Name)
		fmt.Fprintln(mdBuf)
		podName := fmt.Sprintf("github-%s-%s-%s-%s",
			b.Event.PullRequest.Base.Repo.Owner.GetLogin(),
			b.Event.PullRequest.Base.Repo.GetName(),
			b.Event.PullRequest.Head.GetSHA()[:12],
			task.Name)
		podLink := fmt.Sprintf("http://localhost:8001/api/v1/proxy/namespaces/kube-system/services/kubernetes-dashboard/#!/log/%s/%s/?namespace=%s",
			*kubeNamespace, podName, *kubeNamespace)
		fmt.Fprintf(mdBuf, "- [Pod %s](%s)\n", podName, podLink)
		fmt.Fprintln(mdBuf)
		fmt.Fprintln(mdBuf)
	}

	b.Gist.Files["build.md"] = github.GistFile{
		Type:    github.String("text/markdown"),
		Content: github.String(string(mdBuf.Bytes())),
	}

	gist, _, err := githubClient.Gists.Create(ctx, b.Gist)
	if err != nil {
		return fmt.Errorf("cannot write gist: %v", err)
	}
	b.Gist = gist
	b.TargetURL = b.Gist.GetHTMLURL()
	return nil
}

func (b *Builder) startTask(ctx context.Context, task TaskConfig) error {
	statusContext := *statusContext + task.Name
	status := &github.RepoStatus{
		State:       github.String("pending"),
		TargetURL:   github.String(b.TargetURL),
		Description: github.String("started"),
		Context:     github.String(statusContext),
	}
	_, _, err := githubClient.Repositories.CreateStatus(ctx,
		b.Event.PullRequest.Base.Repo.Owner.GetLogin(),
		b.Event.PullRequest.Base.Repo.GetName(),
		b.Event.PullRequest.Head.GetSHA(),
		status,
	)
	if err != nil {
		return fmt.Errorf("cannot create status: %v", err)
	}

	if err := b.runTask(ctx, task); err != nil {
		status.State = github.String("error")
		status.Description = github.String(err.Error())
		_, _, err = githubClient.Repositories.CreateStatus(ctx,
			b.Event.PullRequest.Base.Repo.Owner.GetLogin(),
			b.Event.PullRequest.Base.Repo.GetName(),
			b.Event.PullRequest.Head.GetSHA(),
			status,
		)
		if err != nil {
			return fmt.Errorf("cannot create status: %v", err)
		}
	}
	return nil
}

func (b *Builder) runTask(ctx context.Context, task TaskConfig) error {
	image := b.Config.Image
	if task.Image != "" {
		image = task.Image
	}

	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("triggr-%s-%s-%s-%s",
				b.Event.PullRequest.Base.Repo.Owner.GetLogin(),
				b.Event.PullRequest.Base.Repo.GetName(),
				b.Event.PullRequest.Head.GetSHA()[:12],
				task.Name),
			Labels: map[string]string{
				"triggr": "true",
				"task":   task.Name,
				"repo":   b.Event.PullRequest.Base.Repo.GetFullName(),
				"owner":  b.Event.PullRequest.Base.Repo.Owner.GetLogin(),
			},
			Annotations: map[string]string{
				"triggr.crewjam.com/github-target-url":     b.TargetURL,                                    // e.g.  "http://some-url/job/12345"
				"triggr.crewjam.com/github-last-status":    "pending",                                      // e.g.   "pending"
				"triggr.crewjam.com/github-status-context": *statusContext + task.Name,                     // e.g.   "os-triggr-build"
				"triggr.crewjam.com/github-owner":          b.Event.PullRequest.Base.Repo.Owner.GetLogin(), // e.g.    "crewjam"
				"triggr.crewjam.com/github-repo":           b.Event.PullRequest.Base.Repo.GetName(),        // e.g.     "hello"
				"triggr.crewjam.com/github-ref":            b.Event.PullRequest.Head.GetSHA(),              // e.g.      "adc83b19e793491b1c6ea0fd8b46cd9f32e592fc"
				"triggr.crewjam.com/task-name":             task.Name,
				"triggr.crewjam.com/output-gist":           b.Gist.GetID(),
				"triggr.crewjam.com/output-gist-file-name": task.Name + "-output.txt",
			},
		},
		Spec: v1.PodSpec{
			RestartPolicy: v1.RestartPolicyNever,
			Containers: []v1.Container{
				{
					Name:  "exec",
					Image: image,
					Args:  task.Command,
					Env: []v1.EnvVar{
						{
							Name:  "TRIGGR",
							Value: "true",
						},
						{
							Name: "GIT_CLONE_URL",
							Value: fmt.Sprintf("https://%s:@github.com/%s.git",
								*githubAccessToken, b.Event.PullRequest.Base.Repo.GetFullName()),
						},
						{
							Name:  "GIT_REF",
							Value: fmt.Sprintf("refs/pull/%d/merge", b.Event.PullRequest.GetNumber()),
						},
						{
							Name:  "TASK_NAME",
							Value: task.Name,
						},
						{
							Name:  "GITHUB_OWNER",
							Value: b.Event.PullRequest.Base.Repo.Owner.GetLogin(),
						},
						{
							Name:  "GITHUB_NAME",
							Value: b.Event.PullRequest.Base.Repo.GetName(),
						},
						{
							Name:  "GITHUB_REPO",
							Value: b.Event.PullRequest.Base.Repo.GetFullName(),
						},
						{
							Name:  "GIT_REVISION",
							Value: b.Event.PullRequest.Head.GetSHA(),
						},
						{
							Name:  "GITHUB_STATUS_TARGET_URL",
							Value: b.TargetURL,
						},
						{
							Name:  "GITHUB_STATUS_CONTEXT",
							Value: *statusContext + task.Name,
						},
						{
							Name:  "GITHUB_ACCESS_TOKEN",
							Value: *githubAccessToken,
						},
						{
							Name:  "GIST_ID",
							Value: b.Gist.GetID(),
						},
						{
							Name:  "GIST_FILE_NAME",
							Value: task.Name + " output",
						},
					},
					ImagePullPolicy: "Always",
				},
			},
		},
	}

	pod, err := kubeClient.CoreV1().Pods(*kubeNamespace).Create(pod)
	if err != nil {
		return err
	}
	log.Print("created pod", pod.GetName())
	return nil
}
