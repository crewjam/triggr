package main

import (
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"time"

	"github.com/golang/glog"
	"github.com/google/go-github/github"
	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

type Controller struct {
	indexer  cache.Indexer
	queue    workqueue.RateLimitingInterface
	informer cache.Controller
}

func NewController(queue workqueue.RateLimitingInterface, indexer cache.Indexer, informer cache.Controller) *Controller {
	return &Controller{
		informer: informer,
		indexer:  indexer,
		queue:    queue,
	}
}

func (c *Controller) processNextItem() bool {
	key, quit := c.queue.Get()
	if quit {
		return false
	}
	defer c.queue.Done(key)
	err := c.syncToStdout(key.(string))
	c.handleErr(err, key)
	return true
}

// syncToStdout is the business logic of the controller. In this controller it simply prints
// information about the pod to stdout. In case an error happened, it has to simply return the error.
// The retry logic should not be part of the business logic.
func (c *Controller) syncToStdout(key string) error {
	ctx := context.Background()

	obj, exists, err := c.indexer.GetByKey(key)
	if err != nil {
		glog.Errorf("Fetching object with key %s from store failed with %v", key, err)
		return err
	}
	if !exists {
		return nil
	}

	pod := obj.(*v1.Pod)
	log.Printf("namespace: %s pod: %s", pod.GetNamespace(), pod.GetName())

	annotations := pod.GetObjectMeta().GetAnnotations()
	if annotations["triggr.crewjam.com/github-status-context"] == "" {
		return nil
	}

	githubState := "pending"
	for _, containerStatus := range pod.Status.ContainerStatuses {
		t := containerStatus.State.Terminated
		if t == nil {
			continue
		}
		if t.Reason == "Completed" {
			githubState = "success"
		} else if t.Reason == "Error" {
			githubState = "failure"
		} else {
			githubState = "error"
		}
		break
	}
	if annotations["triggr.crewjam.com/github-last-status"] == githubState {
		fmt.Printf("%s: githubState is unchanged %s\n", pod.GetName(), githubState)
		return nil
	}

	// capture logs and update gist
	gistID := annotations["triggr.crewjam.com/output-gist"]
	if githubState != "pending" && gistID != "" {
		gistFileName := annotations["triggr.crewjam.com/output-gist-file-name"]
		if gistFileName == "" {
			gistFileName = pod.GetName() + ".txt"
		}

		req := kubeClient.CoreV1().RESTClient().Get().
			Namespace(pod.GetNamespace()).
			Name(pod.GetName()).
			Resource("pods").
			SubResource("log").
			Param("container", pod.Spec.Containers[0].Name)
		readCloser, err := req.Stream()
		if err != nil {
			return fmt.Errorf("cannot read output: %v", err)
		}
		out, err := ioutil.ReadAll(readCloser)
		readCloser.Close()
		if err != nil {
			return fmt.Errorf("cannot read output: %v", err)
		}

		_, _, err = githubClient.Gists.Edit(ctx, gistID, &github.Gist{
			Files: map[github.GistFilename]github.GistFile{
				github.GistFilename(gistFileName): github.GistFile{
					Type:    github.String("text/plain"),
					Content: github.String(string(out)),
				},
			},
		})
		if err != nil {
			return fmt.Errorf("cannot save gist: %v", err)
		}
	}

	// set github state
	{
		status := &github.RepoStatus{
			State:       github.String(githubState),
			TargetURL:   github.String(annotations["triggr.crewjam.com/github-target-url"]),
			Description: github.String(githubState),
			Context:     github.String(annotations["triggr.crewjam.com/github-status-context"]),
		}
		_, _, err = githubClient.Repositories.CreateStatus(ctx,
			annotations["triggr.crewjam.com/github-owner"],
			annotations["triggr.crewjam.com/github-repo"],
			annotations["triggr.crewjam.com/github-ref"],
			status,
		)
		if err != nil {
			glog.Errorf("cannot set status %v", err)
			return err
		}
		fmt.Printf("%s: set state to %s\n", pod.GetName(), githubState)
	}

	if githubState != "pending" {
		fmt.Printf("%s: deleted pod\n", pod.GetName())
		err := kubeClient.CoreV1().Pods(pod.GetNamespace()).Delete(pod.GetName(), nil)
		if err != nil {
			glog.Errorf("cannot delete pod: %v", err)
			return err
		}
	} else {
		pod.ObjectMeta.Annotations["triggr.crewjam.com/github-last-status"] = githubState
		if _, err := kubeClient.CoreV1().Pods(pod.GetNamespace()).Update(pod); err != nil {
			glog.Errorf("cannot update pod: %v", err)
			return err
		}
		fmt.Printf("%s: updated pod\n", pod.GetName())
	}

	// TODO: collect logs, update gist, delete pod

	return nil
}

// handleErr checks if an error happened and makes sure we will retry later.
func (c *Controller) handleErr(err error, key interface{}) {
	if err == nil {
		c.queue.Forget(key)
		return
	}
	if c.queue.NumRequeues(key) < 5 {
		glog.Infof("Error syncing pod %v: %v", key, err)
		c.queue.AddRateLimited(key)
		return
	}
	c.queue.Forget(key)
	runtime.HandleError(err)
	glog.Infof("Dropping pod %q out of the queue: %v", key, err)
}

func (c *Controller) Run(threadiness int, stopCh chan struct{}) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()
	glog.Info("Starting Pod controller")
	go c.informer.Run(stopCh)
	if !cache.WaitForCacheSync(stopCh, c.informer.HasSynced) {
		runtime.HandleError(fmt.Errorf("Timed out waiting for caches to sync"))
		return
	}
	for i := 0; i < threadiness; i++ {
		go wait.Until(c.runWorker, time.Second, stopCh)
	}
	<-stopCh
	glog.Info("Stopping Pod controller")
}

func (c *Controller) runWorker() {
	for c.processNextItem() {
	}
}

func runController() {
	podListWatcher := cache.NewListWatchFromClient(
		kubeClient.CoreV1().RESTClient(),
		"pods", *kubeNamespace, fields.Everything())
	queue := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())
	indexer, informer := cache.NewIndexerInformer(podListWatcher, &v1.Pod{}, 0, cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			key, err := cache.MetaNamespaceKeyFunc(obj)
			if err == nil {
				queue.Add(key)
			}
		},
		UpdateFunc: func(old interface{}, new interface{}) {
			key, err := cache.MetaNamespaceKeyFunc(new)
			if err == nil {
				queue.Add(key)
			}
		},
		DeleteFunc: func(obj interface{}) {
			// IndexerInformer uses a delta queue, therefore for deletes we have to use this
			// key function.
			key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
			if err == nil {
				queue.Add(key)
			}
		},
	}, cache.Indexers{})

	controller := NewController(queue, indexer, informer)
	stop := make(chan struct{})
	defer close(stop)
	go controller.Run(1, stop)
	select {}
}
