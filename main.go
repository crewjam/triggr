package main

import (
	"context"
	"flag"
	"log"
	"os"

	"github.com/google/go-github/github"
	"golang.org/x/oauth2"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

var (
	listenAddress = flag.String("listen",
		os.Getenv("LISTEN"),
		"Address to listen on")
	githubAccessToken = flag.String("github-access-token",
		os.Getenv("GITHUB_ACCESS_TOKEN"),
		"The personal access token to manipulate github")
	githubWebhookSecret = flag.String("github-webhook-secret",
		os.Getenv("GITHUB_WEBHOOK_SECRET"),
		"the github webhook secret")
	statusContext = flag.String("github-status-context",
		os.Getenv("GITHUB_STATUS_CONTEXT"),
		"The name of this application, unique from others")
	kubeNamespace = flag.String("namespace",
		os.Getenv("K8S_NAMESPACE"),
		"The kubernetes namespace to use")
	kubeConfigPath = flag.String("kubeconfig", "",
		"absolute path to the kubeconfig file")
	kubeMasterURL = flag.String("master", "",
		"master url")
	githubClient *github.Client
	kubeClient   *kubernetes.Clientset
)

func main() {
	flag.Parse()

	// initialize kubernetes client
	{
		config, err := clientcmd.BuildConfigFromFlags(*kubeMasterURL, *kubeConfigPath)
		if err != nil {
			log.Fatalf("cannot create k8s config: %v", err)
		}
		kubeClient, err = kubernetes.NewForConfig(config)
		if err != nil {
			log.Fatalf("cannot create k8s client: %v", err)
		}
	}

	// initialize github client
	{
		githubClient = github.NewClient(
			oauth2.NewClient(context.Background(),
				oauth2.StaticTokenSource(
					&oauth2.Token{AccessToken: *githubAccessToken},
				),
			),
		)
		if _, _, err := githubClient.Zen(context.Background()); err != nil {
			log.Fatalf("cannot connect to github: %v", err)
		}
	}

	// start the kubernetes controller
	go runController()

	// start the hook server
	go runServer()

	// wait forever
	select {}
}
