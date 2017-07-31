package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"

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
	case *github.PushEvent:
		if event.GetRef() == "refs/heads/master" {
			err := handlePush(r.Context(), event)
			if err != nil {
				log.Printf("handlePush: %v", err)
			}
		}
		return err

	}
	return nil
}

type Builder struct {
	//Event     *github.PullRequestEvent
	Repo        Repo
	SHA         string
	Ref         string
	Owner       string
	Gist        *github.Gist
	Config      Config
	TargetURL   string
	PullRequest *github.PullRequest
}

type Config struct {
	Image string
	Tasks []TaskConfig `toml:"task"`
}

type TaskConfig struct {
	Name          string
	Image         string
	Command       []string
	MapDockerSock bool `toml:"map-docker-sock"` // Danger, Will Robinson.
}

type Repo interface {
	GetFullName() string
	GetName() string
}

func handlePush(ctx context.Context, event *github.PushEvent) error {
	b := Builder{
		Repo:  event.Repo,
		SHA:   event.HeadCommit.GetID(),
		Ref:   event.GetRef(),
		Owner: event.Repo.Owner.GetName(),
		Gist: &github.Gist{
			Description: github.String("Build Status"),
			Public:      github.Bool(false),
			Files:       map[github.GistFilename]github.GistFile{},
		},
	}
	return b.Build(ctx)
}

func handlePullRequest(ctx context.Context, event *github.PullRequestEvent) error {
	b := Builder{
		Repo:        event.PullRequest.Base.Repo,
		PullRequest: event.PullRequest,
		SHA:         event.PullRequest.Head.GetSHA(),
		Ref:         fmt.Sprintf("refs/pull/%d/merge", event.PullRequest.GetNumber()),
		Owner:       event.PullRequest.Base.Repo.Owner.GetLogin(),
		Gist: &github.Gist{
			Description: github.String(event.PullRequest.Base.Repo.GetFullName() + " Build Status"),
			Public:      github.Bool(false),
			Files:       map[github.GistFilename]github.GistFile{},
		},
	}
	return b.Build(ctx)
}

func (b *Builder) Build(ctx context.Context) error {
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
		b.Owner,
		b.Repo.GetName(),
		".triggr.toml",
		&github.RepositoryContentGetOptions{
			Ref: b.SHA,
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
		b.Repo.GetFullName(),
		b.Repo.GetFullName())
	fmt.Fprintln(mdBuf)

	if b.PullRequest != nil {
		fmt.Fprintf(mdBuf, "PR: [#%d %s](%s)\n",
			b.PullRequest.GetNumber(),
			b.PullRequest.GetTitle(),
			b.PullRequest.GetHTMLURL())
		fmt.Fprintln(mdBuf)
	}
	fmt.Fprintf(mdBuf, "Commit: [%s](%s)\n",
		b.SHA,
		fmt.Sprintf("https://github.com/%s/commit/%s",
			b.Repo.GetFullName(),
			b.SHA))
	fmt.Fprintln(mdBuf)
	pretty.Fprintf(mdBuf, "Hack:\n"+
		"```\n"+
		"%# v\n"+
		"```\n\n", b.Config)
	fmt.Fprintln(mdBuf)

	for _, task := range b.Config.Tasks {
		fmt.Fprintln(mdBuf, "## Task", task.Name)
		fmt.Fprintln(mdBuf)
		podName := fmt.Sprintf("triggr-%s-%s-%s-%s",
			b.Owner,
			b.Repo.GetName(),
			b.SHA[:12],
			task.Name)
		podLink := fmt.Sprintf("http://localhost:8001/api/v1/proxy/namespaces/kube-system/services/kubernetes-dashboard/#!/log/%s/%s/?namespace=%s",
			*kubeNamespace, podName, *kubeNamespace)
		fmt.Fprintf(mdBuf, "- [Pod %s](%s)\n", podName, podLink)
		fmt.Fprintf(mdBuf, "- Tail Logs: `kubectl --namespace \"%s\" logs \"%s\" -f`\n", *kubeNamespace, podName)
		fmt.Fprintf(mdBuf, "- Info: `kubectl --namespace \"%s\" get pods \"%s\" -o yaml`\n", *kubeNamespace, podName)
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
	statusContext := *statusContext + "-" + task.Name
	status := &github.RepoStatus{
		State:       github.String("pending"),
		TargetURL:   github.String(b.TargetURL),
		Description: github.String("started"),
		Context:     github.String(statusContext),
	}
	_, _, err := githubClient.Repositories.CreateStatus(ctx,
		b.Owner,
		b.Repo.GetName(),
		b.SHA,
		status,
	)
	if err != nil {
		return fmt.Errorf("cannot create status: %v", err)
	}

	if err := b.runTask(ctx, task); err != nil {
		log.Printf("runTask: %v", err)
		status.State = github.String("error")
		status.Description = github.String(err.Error())
		if len(*status.Description) > 140 {
			status.Description = github.String(err.Error()[:130] + "...")
		}
		_, _, err = githubClient.Repositories.CreateStatus(ctx,
			b.Owner,
			b.Repo.GetName(),
			b.SHA,
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
				b.Owner,
				b.Repo.GetName(),
				b.SHA[:12],
				task.Name),
			Labels: map[string]string{
				"triggr": "true",
				"task":   task.Name,
				"repo":   b.Repo.GetName(),
				"owner":  b.Owner,
			},
			Annotations: map[string]string{
				"triggr.crewjam.com/github-target-url":     b.TargetURL,
				"triggr.crewjam.com/github-last-status":    "pending",
				"triggr.crewjam.com/github-status-context": *statusContext + "-" + task.Name,
				"triggr.crewjam.com/github-owner":          b.Owner,
				"triggr.crewjam.com/github-repo":           b.Repo.GetName(),
				"triggr.crewjam.com/github-ref":            b.SHA,
				"triggr.crewjam.com/task-name":             task.Name,
				"triggr.crewjam.com/output-gist":           b.Gist.GetID(),
				"triggr.crewjam.com/output-gist-file-name": "output-" + task.Name + ".txt",
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
								*githubAccessToken, b.Repo.GetFullName()),
						},
						{
							Name:  "GIT_REF",
							Value: b.Ref,
						},
						{
							Name:  "TASK_NAME",
							Value: task.Name,
						},
						{
							Name:  "GITHUB_OWNER",
							Value: b.Owner,
						},
						{
							Name:  "GITHUB_NAME",
							Value: b.Repo.GetName(),
						},
						{
							Name:  "GITHUB_REPO",
							Value: b.Repo.GetFullName(),
						},
						{
							Name:  "GIT_REVISION",
							Value: b.SHA,
						},
						{
							Name:  "GITHUB_STATUS_TARGET_URL",
							Value: b.TargetURL,
						},
						{
							Name:  "GITHUB_STATUS_CONTEXT",
							Value: *statusContext + "-" + task.Name,
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

	// add environment variable for pull request
	if b.PullRequest != nil {
		pod.Spec.Containers[0].Env = append(pod.Spec.Containers[0].Env, v1.EnvVar{
			Name:  "PULL_REQUEST",
			Value: strconv.Itoa(b.PullRequest.GetNumber()),
		})
	}

	// see about build secrets
	{
		secretWhen := "never"
		if b.PullRequest != nil {
			secretWhen = "pull-request"
		}
		if b.Ref == "refs/heads/master" {
			secretWhen = "master"
		}
		secrets, err := kubeClient.CoreV1().Secrets(*kubeNamespace).List(metav1.ListOptions{
			LabelSelector: "owner=" + b.Owner + ",repo=" + b.Repo.GetName() + ",when=" + secretWhen,
		})
		if err != nil {
			return fmt.Errorf("cannot find secret: %v", err)
		}
		if len(secrets.Items) > 1 {
			return fmt.Errorf("more than one secret matches labels")
		}
		if len(secrets.Items) == 1 {
			secret := secrets.Items[0]

			pod.Spec.Volumes = append(pod.Spec.Volumes, v1.Volume{
				Name: "build-secrets",
				VolumeSource: v1.VolumeSource{
					Secret: &v1.SecretVolumeSource{
						SecretName: secret.GetName(),
					},
				},
			})
			pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, v1.VolumeMount{
				Name:      "build-secrets",
				MountPath: "/var/run/secret/build",
			})

			pod.Spec.Containers[0].Env = append(pod.Spec.Containers[0].Env, v1.EnvVar{
				Name:  "BUILD_SECRETS",
				Value: "/var/run/secret/build",
			})
		}
	}

	if task.MapDockerSock {
		pod.Spec.Volumes = append(pod.Spec.Volumes, v1.Volume{
			Name: "docker-sock",
			VolumeSource: v1.VolumeSource{
				HostPath: &v1.HostPathVolumeSource{
					Path: "/var/run/docker.sock",
				},
			},
		})
		pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, v1.VolumeMount{
			Name:      "docker-sock",
			MountPath: "/var/run/docker.sock",
		})
	}

	pod, err := kubeClient.CoreV1().Pods(*kubeNamespace).Create(pod)
	if err != nil {
		return err
	}
	log.Print("created pod", pod.GetName())
	return nil
}
