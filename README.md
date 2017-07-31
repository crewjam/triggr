
# triggr

Triggr is a simple tool to run pods in Kubernetes when code gets 
pushed to Github. (hence the name, but without the 'e' because dot-com)

The basic flow goes something like this:

* A github webhook calls a service running in Kubernetes

* The webhook grabs `.triggr.toml` from the repository which 
  describes which pods should be created.

* The pods are created, each pod corresponding to a build status for the
  commit.

* If the pod finishes cleanly, then the build status is set to success,
  otherwise it is set to failure.

* The output of each pod is captured in a gist, which is linked from
  the build status.

## Security Warning

I haven't given much thought to the security boundaries or risks 
in running **untrusted** code such as pull requests from random
people, nor the implications of using this on public repositories.
Honestly, for public stuff, you might be better off with Travis.

## Installing

1. Create an access token at https://github.com/settings/tokens. The token should have *gist* and *repo* permissions.

2. Create the kubernetes deployment:

```
# store some secrets
kubectl create secret generic github \
    --from-literal=webhook-secret=A-RANDOM-SECRET-YOU-GENERATE \
    --from-literal=access-token=YOUR-ACCESS-TOKEN

# create the namespace where tasks will run (separate from the server)
kubectl create ns triggr

kubectl apply -f deploy.yaml
```

Wait for the external IP to be available:

```
kubectl get service triggr -w
```

3. Create a webhook in github, *Settings*, *Webhooks*, *Add WebHook*.  

- Set the URL to *http:// **SERVICE-EXTERNAL-IP** /event*.
- Set the Content type to *application/json*.
- Set the secret to the random value you generated before
- Choose "Let me select individual events." and pick *Push* and *Pull Request*

Note: TLS is left as an exercise for the reader.

## Configuring the repository

Place a call called `.triggr.toml` in the root of the repository. It 
should look something like this:

```
image = "crewjam/triggr-build-go"

[[task]]
name = "lint"
image = "yourself/customcontainer"
command = ["golint -set_exit_status ./..."]

[[task]]
name = "test"
command = ["go", "test", "./..."]
```

## Build Secrets

The container will search for a Kubernetes secret that has labels corresponding 
to  the situation and mount it in the container. The environment variable 
`BUILD_SECRETS` contains the directory where these secrets are located. The 
secret must be labled with `owner` = *your-user-id*, `repo` = *the name of the 
repository* and `when` = either `pull-request` or `master` depending on when it
should be used.

Example:

```
kubectl --namespace triggr create secret generic hello-pr \
  --from-file=gcloud-credentials=gcp_auth
kubectl --namespace triggr label secret/hello-pr owner=crewjam repo=hello when=pr

kubectl --namespace triggr create secret generic hello-master \
  --from-file=gcloud-credentials=gcp_auth
kubectl --namespace triggr label secret/hello-master owner=crewjam repo=hello when=master

```
